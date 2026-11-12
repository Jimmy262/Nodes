package panel

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const rememberDuration = 30 * 24 * time.Hour
const browserDuration = 12 * time.Hour
const passwordParameters = "m=65536,t=3,p=2"
const sessionCookie = "nodo_session"
const setupCookie = "nodo_setup"

type loginSession struct {
	Hash     string
	Expires  int64
	Remember bool
}
type passwordInput struct {
	Password string `json:"password"`
	Confirm  string `json:"confirmPassword"`
	Current  string `json:"currentPassword"`
	Remember bool   `json:"remember"`
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

// Fail before opening the HTTP listener if platform protection is unavailable.
func ValidateSecretProtection() error {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	defer clear(secret)
	protected, err := protectSecret(secret)
	if err != nil {
		return err
	}
	recovered, err := unprotectSecret(protected)
	if err != nil {
		return err
	}
	defer clear(recovered)
	if subtle.ConstantTimeCompare(secret, recovered) != 1 {
		return errors.New("No se pudo comprobar la protección de credenciales")
	}
	return nil
}
func tokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
func validToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}
func validatePassword(password, confirmation string) error {
	if password != confirmation {
		return errors.New("Los PIN no coinciden")
	}
	if !validPIN(password) {
		return errors.New("El PIN debe tener exactamente 8 números")
	}
	if strings.Count(password, string(password[0])) == 8 || password == "12345678" || password == "87654321" || password == "01234567" || password == "12121212" {
		return errors.New("Evita secuencias conocidas o un único número repetido")
	}
	return nil
}
func validPIN(pin string) bool {
	if len(pin) != 8 {
		return false
	}
	for _, digit := range pin {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
func pinSecret(pin string, pepper []byte) []byte {
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte("nodo-pin-v1:" + pin))
	return mac.Sum(nil)
}
func hashPassword(password string) (string, error) {
	pepper := make([]byte, 32)
	if _, err := rand.Read(pepper); err != nil {
		return "", err
	}
	defer clear(pepper)
	protected, err := protectSecret(pepper)
	if err != nil {
		return "", err
	}
	secret := pinSecret(password, pepper)
	defer clear(secret)
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	digest := argon2.IDKey(secret, salt, 3, 64*1024, 2, 32)
	phc := "$argon2id$v=19$" + passwordParameters + "$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(digest)
	return protectedHashPrefix + base64.RawStdEncoding.EncodeToString(protected) + ":" + phc, nil
}
func verifyPassword(password, encoded string) bool {
	if !validPIN(password) || !strings.HasPrefix(encoded, protectedHashPrefix) || len(encoded) > 8192 {
		return false
	}
	blob, phc, ok := strings.Cut(strings.TrimPrefix(encoded, protectedHashPrefix), ":")
	if !ok {
		return false
	}
	protected, err := base64.RawStdEncoding.DecodeString(blob)
	if err != nil {
		return false
	}
	pepper, err := unprotectSecret(protected)
	if err != nil {
		return false
	}
	defer clear(pepper)
	secret := pinSecret(password, pepper)
	defer clear(secret)
	parts := strings.Split(phc, "$")
	// Bound the parameters before allocating memory, including for damaged databases.
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != passwordParameters {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey(secret, salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}
func (a *App) PasswordConfigured() (bool, error) {
	var exists bool
	err := a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM auth_credentials WHERE id=1)").Scan(&exists)
	return exists, err
}
func (a *App) session(r *http.Request) (*loginSession, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || !validToken(cookie.Value) {
		return nil, nil
	}
	session := loginSession{Hash: tokenHash(cookie.Value)}
	err = a.db.QueryRow("SELECT expires_at,remember FROM auth_sessions WHERE token_hash=? AND expires_at>? AND EXISTS(SELECT 1 FROM auth_credentials WHERE id=1)", session.Hash, time.Now().Unix()).Scan(&session.Expires, &session.Remember)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// Caller holds authMu. Setup authorization is separate from a panel session.
func (a *App) hasSetupGrant(r *http.Request) bool {
	cookie, err := r.Cookie(setupCookie)
	return err == nil && a.setupToken != "" && time.Now().Before(a.setupUntil) && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(a.setupToken)) == 1
}
func (a *App) authStatus(w http.ResponseWriter, r *http.Request) {
	configured, err := a.PasswordConfigured()
	if err != nil {
		fail(w, 503, "No se pudo leer la configuración de acceso")
		return
	}
	session, err := a.session(r)
	if err != nil {
		fail(w, 503, "No se pudo comprobar la sesión")
		return
	}
	status := map[string]any{"configured": configured, "authenticated": session != nil, "setupAuthorized": false}
	if !configured {
		a.authMu.Lock()
		status["setupAuthorized"] = a.hasSetupGrant(r)
		a.authMu.Unlock()
	}
	if session != nil {
		status["remember"] = session.Remember
		status["expiresAt"] = time.Unix(session.Expires, 0).UTC().Format(time.RFC3339)
	}
	reply(w, 200, status)
}
func authBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	fail(w, 429, "Hay otra comprobación de acceso en curso. Inténtalo de nuevo en un segundo")
}
func (a *App) throttle(w http.ResponseWriter) bool {
	var blocked int64
	if err := a.db.QueryRow("SELECT blocked_until FROM auth_throttle WHERE id=1").Scan(&blocked); err != nil {
		fail(w, 503, "No se pudo comprobar el límite de intentos")
		return false
	}
	seconds := blocked - time.Now().Unix()
	if seconds > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		fail(w, 429, fmt.Sprintf("Demasiados intentos. Espera %d segundos", seconds))
		return false
	}
	return true
}
func (a *App) failedLogin(w http.ResponseWriter) {
	now := time.Now().Unix()
	var failures int
	var start int64
	if err := a.db.QueryRow("SELECT failures,window_start FROM auth_throttle WHERE id=1").Scan(&failures, &start); err != nil {
		fail(w, 503, "No se pudo registrar el intento")
		return
	}
	if now-start >= 15*60 {
		failures = 0
		start = now
	}
	failures++
	blocked := int64(0)
	if failures >= 5 {
		delay := int64(60)
		for i := 5; i < failures && delay < 900; i++ {
			delay *= 2
		}
		if delay > 900 {
			delay = 900
		}
		blocked = now + delay
	}
	if _, err := a.db.Exec("UPDATE auth_throttle SET failures=?,window_start=?,blocked_until=? WHERE id=1", failures, start, blocked); err != nil {
		fail(w, 503, "No se pudo registrar el intento")
		return
	}
	if blocked > now {
		w.Header().Set("Retry-After", strconv.FormatInt(blocked-now, 10))
		fail(w, 429, fmt.Sprintf("Demasiados intentos. Espera %d segundos", blocked-now))
		return
	}
	fail(w, 401, "PIN incorrecto")
}
func (a *App) prepareSetup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token string `json:"setupToken"`
	}
	if decode(w, r, &input) != nil {
		fail(w, 400, "Datos inválidos")
		return
	}
	if !a.authMu.TryLock() {
		authBusy(w)
		return
	}
	defer a.authMu.Unlock()
	configured, err := a.PasswordConfigured()
	if err != nil {
		fail(w, 503, "No se pudo comprobar el acceso")
		return
	}
	if configured {
		fail(w, 409, "El PIN ya está configurado. Inicia sesión")
		return
	}
	if a.setupToken == "" || subtle.ConstantTimeCompare([]byte(input.Token), []byte(a.setupToken)) != 1 {
		fail(w, 403, "El enlace de configuración no es válido. Abre el enlace de la consola actual")
		return
	}
	a.setupUntil = time.Now().Add(15 * time.Minute)
	http.SetCookie(w, &http.Cookie{Name: setupCookie, Value: a.setupToken, Path: "/api/auth/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 900, Expires: a.setupUntil, Secure: a.secureRequest(r)})
	reply(w, 200, map[string]bool{"ok": true})
}
func newSession(remember bool) (string, loginSession, error) {
	token, err := randomToken()
	if err != nil {
		return "", loginSession{}, err
	}
	duration := browserDuration
	if remember {
		duration = rememberDuration
	}
	return token, loginSession{Hash: tokenHash(token), Expires: time.Now().Add(duration).Unix(), Remember: remember}, nil
}
func insertSession(tx *sql.Tx, session loginSession, r *http.Request) error {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if _, err = tx.Exec("DELETE FROM auth_sessions WHERE token_hash=?", tokenHash(cookie.Value)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("DELETE FROM auth_sessions WHERE expires_at<=?", time.Now().Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO auth_sessions(token_hash,expires_at,remember,created_at) VALUES(?,?,?,?)", session.Hash, session.Expires, session.Remember, time.Now().Unix()); err != nil {
		return err
	}
	_, err := tx.Exec("UPDATE auth_throttle SET failures=0,window_start=0,blocked_until=0 WHERE id=1")
	return err
}
func (a *App) sessionResponse(w http.ResponseWriter, r *http.Request, token string, session loginSession) {
	cookie := &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: a.secureRequest(r)}
	if session.Remember {
		cookie.MaxAge = int(rememberDuration.Seconds())
		cookie.Expires = time.Unix(session.Expires, 0)
	}
	http.SetCookie(w, cookie)
	reply(w, 200, map[string]any{"ok": true, "remember": session.Remember, "expiresAt": time.Unix(session.Expires, 0).UTC().Format(time.RFC3339)})
}
func (a *App) clearCookie(w http.ResponseWriter, r *http.Request, name, path string) {
	http.SetCookie(w, &http.Cookie{Name: name, Path: path, MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: a.secureRequest(r)})
}

func (a *App) setupPassword(w http.ResponseWriter, r *http.Request) {
	var input passwordInput
	if decode(w, r, &input) != nil {
		fail(w, 400, "Datos inválidos")
		return
	}
	if !a.authMu.TryLock() {
		authBusy(w)
		return
	}
	defer a.authMu.Unlock()
	configured, err := a.PasswordConfigured()
	if err != nil {
		fail(w, 503, "No se pudo comprobar el acceso")
		return
	}
	if configured {
		fail(w, 409, "El PIN ya está configurado")
		return
	}
	if !a.hasSetupGrant(r) {
		fail(w, 403, "Abre el enlace de configuración de la consola para crear tu PIN")
		return
	}
	if err := validatePassword(input.Password, input.Confirm); err != nil {
		fail(w, 400, err.Error())
		return
	}
	encoded, err := hashPassword(input.Password)
	if err != nil {
		fail(w, 500, "No se pudo proteger el PIN")
		return
	}
	token, session, err := newSession(input.Remember)
	if err != nil {
		fail(w, 500, "No se pudo crear la sesión")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, "No se pudo guardar el acceso")
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO auth_credentials(id,password_hash,updated_at) VALUES(1,?,?)", encoded, time.Now().Unix()); err != nil {
		fail(w, 409, "No se pudo crear el acceso. Comprueba si ya está configurado")
		return
	}
	if err = insertSession(tx, session, r); err != nil {
		fail(w, 500, "No se pudo guardar la sesión")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "No se pudo guardar el acceso")
		return
	}
	a.setupToken = ""
	a.setupUntil = time.Time{}
	a.clearCookie(w, r, setupCookie, "/api/auth/")
	a.sessionResponse(w, r, token, session)
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if decode(w, r, &input) != nil {
		fail(w, 400, "Datos inválidos")
		return
	}
	if !a.authMu.TryLock() {
		authBusy(w)
		return
	}
	defer a.authMu.Unlock()
	if !a.throttle(w) {
		return
	}
	var encoded string
	err := a.db.QueryRow("SELECT password_hash FROM auth_credentials WHERE id=1").Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 409, "Crea tu PIN usando el enlace de configuración de la consola")
		return
	}
	if err != nil {
		fail(w, 503, "No se pudo comprobar el acceso")
		return
	}
	if !verifyPassword(input.Password, encoded) {
		a.failedLogin(w)
		return
	}
	token, session, err := newSession(input.Remember)
	if err != nil {
		fail(w, 500, "No se pudo crear la sesión")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, "No se pudo guardar la sesión")
		return
	}
	defer tx.Rollback()
	if err = insertSession(tx, session, r); err != nil {
		fail(w, 500, "No se pudo guardar la sesión")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "No se pudo guardar la sesión")
		return
	}
	a.sessionResponse(w, r, token, session)
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if _, err = a.db.Exec("DELETE FROM auth_sessions WHERE token_hash=?", tokenHash(cookie.Value)); err != nil {
			fail(w, 503, "No se pudo cerrar la sesión")
			return
		}
	}
	a.clearCookie(w, r, sessionCookie, "/")
	reply(w, 200, map[string]bool{"ok": true})
}
func (a *App) changePassword(w http.ResponseWriter, r *http.Request) {
	var input passwordInput
	if decode(w, r, &input) != nil {
		fail(w, 400, "Datos inválidos")
		return
	}
	if !a.authMu.TryLock() {
		authBusy(w)
		return
	}
	defer a.authMu.Unlock()
	current, err := a.session(r)
	if err != nil {
		fail(w, 503, "No se pudo comprobar la sesión")
		return
	}
	if current == nil {
		fail(w, 401, "Inicia sesión de nuevo")
		return
	}
	if !a.throttle(w) {
		return
	}
	var encoded string
	if err = a.db.QueryRow("SELECT password_hash FROM auth_credentials WHERE id=1").Scan(&encoded); err != nil {
		fail(w, 503, "No se pudo comprobar el acceso")
		return
	}
	if !verifyPassword(input.Current, encoded) {
		a.failedLogin(w)
		return
	}
	if err = validatePassword(input.Password, input.Confirm); err != nil {
		fail(w, 400, err.Error())
		return
	}
	encoded, err = hashPassword(input.Password)
	if err != nil {
		fail(w, 500, "No se pudo proteger el PIN")
		return
	}
	token, session, err := newSession(current.Remember)
	if err != nil {
		fail(w, 500, "No se pudo crear la sesión")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, "No se pudo cambiar el PIN")
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE auth_credentials SET password_hash=?,updated_at=? WHERE id=1", encoded, time.Now().Unix()); err != nil {
		fail(w, 500, "No se pudo guardar el PIN")
		return
	}
	if _, err = tx.Exec("DELETE FROM auth_sessions"); err != nil {
		fail(w, 500, "No se pudieron cerrar las sesiones anteriores")
		return
	}
	if err = insertSession(tx, session, r); err != nil {
		fail(w, 500, "No se pudo renovar la sesión")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "No se pudo cambiar el PIN")
		return
	}
	a.sessionResponse(w, r, token, session)
}
func (a *App) logoutAll(w http.ResponseWriter, r *http.Request) {
	if !a.authMu.TryLock() {
		authBusy(w)
		return
	}
	defer a.authMu.Unlock()
	current, err := a.session(r)
	if err != nil {
		fail(w, 503, "No se pudo comprobar la sesión")
		return
	}
	if current == nil {
		fail(w, 401, "Inicia sesión de nuevo")
		return
	}
	if _, err = a.db.Exec("DELETE FROM auth_sessions"); err != nil {
		fail(w, 503, "No se pudieron cerrar las sesiones")
		return
	}
	a.clearCookie(w, r, sessionCookie, "/")
	reply(w, 200, map[string]bool{"ok": true})
}

// ResetAuthentication is only called by the explicit local CLI recovery flag.
// Inventory and metrics are retained. Restarting normally never invokes this.
func (a *App) ResetAuthentication() error {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM auth_sessions"); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM auth_credentials"); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE auth_throttle SET failures=0,window_start=0,blocked_until=0 WHERE id=1"); err != nil {
		return err
	}
	a.setupUntil = time.Time{}
	return tx.Commit()
}
