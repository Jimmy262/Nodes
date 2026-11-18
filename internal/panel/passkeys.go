package panel

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const ceremonyCookie = "nodes_passkey_ceremony"
const ceremonyPath = "/api/auth/passkeys/"

type passkeyUser struct {
	handle      []byte
	credentials []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return u.handle }
func (u passkeyUser) WebAuthnName() string                       { return "administrador" }
func (u passkeyUser) WebAuthnDisplayName() string                { return "Administrador de Nodes" }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func (a *App) passkeyConfig(r *http.Request) (*webauthn.WebAuthn, error) {
	origin := a.requestOrigin(r)
	u, err := url.Parse(origin)
	if err != nil || net.ParseIP(u.Hostname()) != nil || (u.Scheme != "https" && u.Hostname() != "localhost") {
		return nil, errors.New("Abre Nodes en http://localhost con su puerto, o en su dominio HTTPS, para usar passkeys")
	}
	return webauthn.New(&webauthn.Config{
		RPID: u.Hostname(), RPDisplayName: "Nodes", RPOrigins: []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
	})
}

func (a *App) passkeySupported(r *http.Request) bool {
	_, err := a.passkeyConfig(r)
	return err == nil
}

func (a *App) passkeyUser(rp string, create bool) (passkeyUser, error) {
	var u passkeyUser
	err := a.db.QueryRow("SELECT handle FROM auth_identity WHERE id=1").Scan(&u.handle)
	if errors.Is(err, sql.ErrNoRows) && create {
		u.handle = make([]byte, 32)
		if _, err = rand.Read(u.handle); err == nil {
			_, err = a.db.Exec("INSERT INTO auth_identity VALUES(1,?)", u.handle)
		}
	}
	if err != nil {
		return u, err
	}
	rows, err := a.db.Query("SELECT credential FROM auth_passkeys WHERE rp_id=?", rp)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var encoded string
		var c webauthn.Credential
		if err = rows.Scan(&encoded); err != nil {
			return u, err
		}
		if err = json.Unmarshal([]byte(encoded), &c); err != nil {
			return u, err
		}
		u.credentials = append(u.credentials, c)
	}
	return u, rows.Err()
}

// Security-method changes require a passkey verification from the last five minutes.
func (a *App) recentSession(r *http.Request) (*loginSession, error) {
	s, err := a.session(r)
	if err != nil || s == nil {
		return nil, errors.New("Verifica tu passkey antes de cambiar el acceso")
	}
	var created int64
	if err = a.db.QueryRow("SELECT created_at FROM auth_sessions WHERE token_hash=?", s.Hash).Scan(&created); err != nil {
		return nil, err
	}
	if created < time.Now().Add(-5*time.Minute).Unix() {
		return nil, errors.New("Verifica tu passkey de nuevo antes de cambiar el acceso")
	}
	return s, nil
}

func (a *App) registrationBinding(r *http.Request) (string, error) {
	configured, err := a.AccessConfigured()
	if err != nil {
		return "", err
	}
	if !configured {
		if a.hasSetupGrant(r) {
			return "setup:" + tokenHash(a.setupGrant), nil
		}
		return "", errors.New("Abre el enlace privado de la consola para registrar tu primera passkey")
	}
	s, err := a.recentSession(r)
	if err != nil {
		return "", err
	}
	return "session:" + s.Hash, nil
}

type passkeyCeremony struct {
	session  webauthn.SessionData
	remember bool
	name     string
}

func (a *App) saveCeremony(w http.ResponseWriter, r *http.Request, kind, binding, name string, remember bool, session *webauthn.SessionData, options any) {
	token, err := randomToken()
	if err != nil {
		fail(w, 500, "No se pudo iniciar la verificación")
		return
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		fail(w, 500, "No se pudo iniciar la verificación")
		return
	}
	_, err = a.db.Exec("DELETE FROM auth_ceremonies WHERE expires_at<=?", time.Now().Unix())
	if err == nil {
		if previous, e := r.Cookie(ceremonyCookie); e == nil {
			_, err = a.db.Exec("DELETE FROM auth_ceremonies WHERE token_hash=?", tokenHash(previous.Value))
		}
	}
	var count int
	if err == nil {
		err = a.db.QueryRow("SELECT COUNT(*) FROM auth_ceremonies").Scan(&count)
	}
	if err != nil {
		fail(w, 503, "No se pudo preparar la verificación")
		return
	}
	if count >= 128 {
		authBusy(w)
		return
	}
	_, err = a.db.Exec("INSERT INTO auth_ceremonies VALUES(?,?,?,?,?,?,?,?)", tokenHash(token), kind, a.requestOrigin(r), binding, string(encoded), remember, name, time.Now().Add(3*time.Minute).Unix())
	if err != nil {
		fail(w, 503, "No se pudo preparar la verificación")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: ceremonyCookie, Value: token, Path: ceremonyPath, MaxAge: 180, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: a.secureRequest(r)})
	reply(w, 200, options)
}

// Consume before cryptographic verification: a challenge is valid for one attempt only.
func (a *App) consumeCeremony(w http.ResponseWriter, r *http.Request, kind, binding string) (passkeyCeremony, error) {
	var c passkeyCeremony
	cookie, err := r.Cookie(ceremonyCookie)
	if err != nil || !validToken(cookie.Value) {
		return c, errors.New("Inicia de nuevo la verificación de tu passkey")
	}
	var encoded, storedKind, origin, storedBinding string
	var expires int64
	err = a.db.QueryRow("SELECT kind,origin,binding,session,remember,name,expires_at FROM auth_ceremonies WHERE token_hash=?", tokenHash(cookie.Value)).Scan(&storedKind, &origin, &storedBinding, &encoded, &c.remember, &c.name, &expires)
	if err != nil {
		return c, errors.New("La verificación ya se usó o caducó. Inténtalo otra vez")
	}
	if _, err = a.db.Exec("DELETE FROM auth_ceremonies WHERE token_hash=?", tokenHash(cookie.Value)); err != nil {
		return c, err
	}
	a.clearCookie(w, r, ceremonyCookie, ceremonyPath)
	if storedKind != kind || origin != a.requestOrigin(r) || storedBinding != binding || expires <= time.Now().Unix() {
		return c, errors.New("La verificación caducó o no corresponde a este acceso")
	}
	err = json.Unmarshal([]byte(encoded), &c.session)
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	return c, err
}

func (a *App) beginPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	binding, err := a.registrationBinding(r)
	if err != nil {
		fail(w, 403, err.Error())
		return
	}
	wa, err := a.passkeyConfig(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var input struct {
		Name     string `json:"name"`
		Remember bool   `json:"remember"`
	}
	if decode(w, r, &input) != nil {
		fail(w, 400, "Datos inválidos")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		input.Name = "Mi passkey"
	}
	if len([]rune(input.Name)) > 80 {
		fail(w, 400, "Usa un nombre de hasta 80 caracteres")
		return
	}
	u, err := a.passkeyUser(wa.Config.RPID, true)
	if err != nil {
		fail(w, 503, "No se pudo leer el acceso")
		return
	}
	if len(u.credentials) >= 10 {
		fail(w, 400, "Puedes registrar hasta 10 passkeys")
		return
	}
	exclusions := []protocol.CredentialDescriptor{}
	for _, c := range u.credentials {
		exclusions = append(exclusions, c.Descriptor())
	}
	options, session, err := wa.BeginRegistration(u, webauthn.WithExclusions(exclusions), webauthn.WithExtensions(webauthn.WithExtensionCredProps()))
	if err != nil {
		fail(w, 500, "No se pudo iniciar el registro")
		return
	}
	a.saveCeremony(w, r, "register", binding, input.Name, input.Remember, session, options)
}

func (a *App) finishPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	binding, err := a.registrationBinding(r)
	if err != nil {
		fail(w, 403, err.Error())
		return
	}
	c, err := a.consumeCeremony(w, r, "register", binding)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	wa, err := a.passkeyConfig(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	u, err := a.passkeyUser(wa.Config.RPID, false)
	if err != nil {
		fail(w, 503, "No se pudo leer el acceso")
		return
	}
	if len(u.credentials) >= 10 {
		fail(w, 400, "Puedes registrar hasta 10 passkeys")
		return
	}
	credential, err := wa.FinishRegistration(u, c.session, r)
	if err != nil {
		fail(w, 400, "No se pudo verificar la passkey. Intenta registrarla otra vez")
		return
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		fail(w, 500, "No se pudo guardar la passkey")
		return
	}
	token, session, err := newSession(c.remember)
	if err != nil {
		fail(w, 500, "No se pudo crear la sesión")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 503, "No se pudo guardar el acceso")
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO auth_passkeys VALUES(?,?,?,?,?,?)", base64.RawURLEncoding.EncodeToString(credential.ID), wa.Config.RPID, c.name, string(encoded), time.Now().Unix(), time.Now().Unix())
	if err == nil {
		err = insertSession(tx, session, r)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 409, "No se pudo guardar la passkey. Comprueba si ya está registrada")
		return
	}
	a.setupToken = ""
	a.setupGrant = ""
	a.setupUntil = time.Time{}
	a.clearCookie(w, r, setupCookie, "/api/auth/")
	a.sessionResponse(w, r, token, session)
}

func (a *App) beginPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	wa, err := a.passkeyConfig(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var input struct {
		Remember bool `json:"remember"`
	}
	if decode(w, r, &input) != nil {
		fail(w, 400, "Datos inválidos")
		return
	}
	options, session, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		fail(w, 500, "No se pudo iniciar el acceso")
		return
	}
	a.saveCeremony(w, r, "login", "", "", input.Remember, session, options)
}

func (a *App) finishPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	c, err := a.consumeCeremony(w, r, "login", "")
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	wa, err := a.passkeyConfig(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	credential, err := wa.FinishDiscoverableLogin(func(rawID, handle []byte) (webauthn.User, error) {
		u, err := a.passkeyUser(wa.Config.RPID, false)
		if err != nil || !bytes.Equal(u.handle, handle) {
			return nil, errors.New("Passkey desconocida")
		}
		for _, credential := range u.credentials {
			if bytes.Equal(credential.ID, rawID) {
				return u, nil
			}
		}
		return nil, errors.New("Passkey desconocida")
	}, c.session, r)
	if err != nil || credential.Authenticator.CloneWarning {
		// This challenge is already consumed. A forged assertion must never
		// lock every other browser out of a signature-based account. Ingress
		// quotas are scoped to the client peer, not to the shared account.
		fail(w, 401, "No se pudo verificar la passkey")
		return
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		fail(w, 500, "No se pudo actualizar la passkey")
		return
	}
	token, session, err := newSession(c.remember)
	if err != nil {
		fail(w, 500, "No se pudo crear la sesión")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 503, "No se pudo guardar la sesión")
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec("UPDATE auth_passkeys SET credential=?,last_used_at=? WHERE id=? AND rp_id=?", string(encoded), time.Now().Unix(), base64.RawURLEncoding.EncodeToString(credential.ID), wa.Config.RPID)
	if err == nil {
		err = insertSession(tx, session, r)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 503, "No se pudo guardar la sesión")
		return
	}
	a.sessionResponse(w, r, token, session)
}

func (a *App) listPasskeys(w http.ResponseWriter, r *http.Request) {
	wa, err := a.passkeyConfig(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	rows, err := a.db.Query("SELECT id,name,created_at,last_used_at FROM auth_passkeys WHERE rp_id=? ORDER BY created_at,id", wa.Config.RPID)
	if err != nil {
		fail(w, 503, "No se pudieron leer tus passkeys")
		return
	}
	defer rows.Close()
	type item struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Created int64  `json:"createdAt"`
		Used    int64  `json:"lastUsedAt"`
	}
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.ID, &v.Name, &v.Created, &v.Used); err != nil {
			fail(w, 503, "No se pudieron leer tus passkeys")
			return
		}
		items = append(items, v)
	}
	if rows.Err() != nil {
		fail(w, 503, "No se pudieron leer tus passkeys")
		return
	}
	reply(w, 200, items)
}

func (a *App) deletePasskey(w http.ResponseWriter, r *http.Request) {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	current, err := a.recentSession(r)
	if err != nil {
		fail(w, 403, err.Error())
		return
	}
	wa, err := a.passkeyConfig(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var count int
	if err = a.db.QueryRow("SELECT COUNT(*) FROM auth_passkeys WHERE rp_id=?", wa.Config.RPID).Scan(&count); err != nil {
		fail(w, 503, "No se pudo comprobar el acceso")
		return
	}
	if count <= 1 {
		fail(w, 409, "Registra otra passkey antes de eliminar la última")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 503, "No se pudo eliminar la passkey")
		return
	}
	defer tx.Rollback()
	result, err := tx.Exec("DELETE FROM auth_passkeys WHERE id=? AND rp_id=?", r.PathValue("id"), wa.Config.RPID)
	if err != nil {
		fail(w, 503, "No se pudo eliminar la passkey")
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		fail(w, 404, "Passkey no encontrada")
		return
	}
	// Revocation closes other sessions, including sessions created with the removed key.
	if _, err = tx.Exec("DELETE FROM auth_sessions WHERE token_hash<>?", current.Hash); err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 503, "No se pudo revocar el acceso")
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
