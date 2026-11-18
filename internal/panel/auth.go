package panel

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"time"
)

const rememberDuration = 30 * 24 * time.Hour
const browserDuration = 12 * time.Hour
const setupDuration = 15 * time.Minute
const sessionCookie = "nodo_session"
const setupCookie = "nodo_setup"

type loginSession struct {
	Hash     string
	Expires  int64
	Remember bool
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
func (a *App) AccessConfigured() (bool, error) {
	var exists bool
	err := a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM auth_passkeys)").Scan(&exists)
	return exists, err
}
func (a *App) session(r *http.Request) (*loginSession, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || !validToken(cookie.Value) {
		return nil, nil
	}
	session := loginSession{Hash: tokenHash(cookie.Value)}
	err = a.db.QueryRow("SELECT expires_at,remember FROM auth_sessions WHERE token_hash=? AND expires_at>? AND EXISTS(SELECT 1 FROM auth_passkeys)", session.Hash, time.Now().Unix()).Scan(&session.Expires, &session.Remember)
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
	return err == nil && a.setupGrant != "" && time.Now().Before(a.setupUntil) && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(a.setupGrant)) == 1
}
func (a *App) authStatus(w http.ResponseWriter, r *http.Request) {
	configured, err := a.AccessConfigured()
	if err != nil {
		fail(w, 503, "No se pudo leer la configuración de acceso")
		return
	}
	session, err := a.session(r)
	if err != nil {
		fail(w, 503, "No se pudo comprobar la sesión")
		return
	}
	status := map[string]any{"configured": configured, "authenticated": session != nil, "setupAuthorized": false, "passkeyAvailable": a.passkeySupported(r)}
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
	configured, err := a.AccessConfigured()
	if err != nil {
		fail(w, 503, "No se pudo comprobar el acceso")
		return
	}
	if configured {
		fail(w, 409, "El acceso ya está configurado. Inicia sesión")
		return
	}
	if a.setupToken == "" || !time.Now().Before(a.setupExpires) || subtle.ConstantTimeCompare([]byte(input.Token), []byte(a.setupToken)) != 1 {
		fail(w, 403, "El enlace de configuración caducó, ya se usó o no es válido. Reinicia Nodes para generar otro")
		return
	}
	grant, err := randomToken()
	if err != nil {
		fail(w, 500, "No se pudo autorizar el registro de tu passkey")
		return
	}
	// Exchange the link once for an independent browser-only secret. Opening it
	// never extends the original deadline, including an in-progress ceremony.
	a.setupToken = ""
	a.setupGrant = grant
	a.setupUntil = a.setupExpires
	remaining := int(time.Until(a.setupUntil).Seconds())
	if remaining < 1 {
		remaining = 1
	}
	http.SetCookie(w, &http.Cookie{Name: setupCookie, Value: grant, Path: "/api/auth/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: remaining, Expires: a.setupUntil, Secure: a.secureRequest(r)})
	reply(w, 200, map[string]any{"ok": true, "expiresAt": a.setupUntil.UTC().Format(time.RFC3339)})
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
	if _, err = tx.Exec("DELETE FROM auth_passkeys; DELETE FROM auth_identity; DELETE FROM auth_ceremonies"); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE auth_throttle SET failures=0,window_start=0,blocked_until=0 WHERE id=1"); err != nil {
		return err
	}
	a.setupUntil = time.Time{}
	a.setupGrant = ""
	return tx.Commit()
}
