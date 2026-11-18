package panel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type managedSSHFiles struct {
	directory  string
	identity   string
	knownHosts string
	keyID      string
}

// Only encrypted Ed25519 seeds persist. OpenSSH gets private runtime files in
// the service's private temp directory (tmpfs in production), never API fields.
func (a *App) loadManagedSSH(s *Server) error {
	var protected, hostKey []byte
	err := a.db.QueryRow("SELECT protected_seed,host_key FROM server_ssh_keys WHERE server_id=?", s.ID).Scan(&protected, &hostKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	cacheKey := tokenHash(string(protected) + s.Host + ":" + strconv.Itoa(s.Port))
	a.sshMu.Lock()
	defer a.sshMu.Unlock()
	files, ok := a.managedSSH[cacheKey]
	if !ok {
		seed, err := unprotectSecret(protected)
		if err != nil {
			return errors.New("No se pudo abrir la credencial SSH protegida")
		}
		defer clear(seed)
		private := ed25519.NewKeyFromSeed(seed)
		defer clear(private)
		signer, err := ssh.NewSignerFromKey(private)
		if err != nil {
			return errors.New("Credencial SSH no válida")
		}
		key, err := ssh.MarshalPrivateKey(private, "nodes-managed")
		if err != nil {
			return errors.New("No se pudo preparar el acceso SSH")
		}
		defer clear(key.Bytes)
		encoded := pem.EncodeToMemory(key)
		defer clear(encoded)
		remoteKey, err := ssh.ParsePublicKey(hostKey)
		if err != nil {
			return errors.New("La identidad guardada del servidor no es válida")
		}
		directory, err := os.MkdirTemp("", "nodes-ssh-")
		if err != nil {
			return errors.New("No se pudo preparar el acceso SSH privado")
		}
		if err = restrictSSHDirectory(directory); err != nil {
			os.RemoveAll(directory)
			return errors.New("No se pudieron restringir los permisos de la credencial SSH")
		}
		files = managedSSHFiles{directory: directory, identity: filepath.Join(directory, "identity"), knownHosts: filepath.Join(directory, "known_hosts"), keyID: ssh.FingerprintSHA256(signer.PublicKey())}
		address := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
		line := knownhosts.Line([]string{address}, remoteKey) + "\n"
		if err = os.WriteFile(files.identity, encoded, 0600); err == nil {
			err = os.WriteFile(files.knownHosts, []byte(line), 0600)
		}
		if err != nil {
			os.RemoveAll(directory)
			return errors.New("No se pudo preparar el acceso SSH privado")
		}
		if a.managedSSH == nil {
			a.managedSSH = make(map[string]managedSSHFiles)
		}
		a.managedSSH[cacheKey] = files
	}
	s.ManagedSSH = true
	s.sshIdentity, s.sshKnownHosts, s.sshKeyID = files.identity, files.knownHosts, files.keyID
	return nil
}

func (a *App) closeManagedSSH() {
	a.sshMu.Lock()
	defer a.sshMu.Unlock()
	for key, files := range a.managedSSH {
		os.RemoveAll(files.directory)
		delete(a.managedSSH, key)
	}
}

type enrollmentInput struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	User      string `json:"user"`
	Token     string `json:"token"`
	Password  string `json:"password"`
	Confirmed bool   `json:"confirmed"`
}

func (input enrollmentInput) server() Server {
	return Server{Name: strings.TrimSpace(input.Name), Host: strings.TrimSpace(input.Host), Port: input.Port, User: strings.TrimSpace(input.User)}
}

func enrollmentBinding(s Server) string {
	return tokenHash(s.Name + "\x00" + s.Host + "\x00" + strconv.Itoa(s.Port) + "\x00" + s.User)
}

// A deadline applies to dial, handshake and remote commands. Canceling the
// request also closes the connection, including while a server stalls.
func enrollmentClient(ctx context.Context, s Server, config *ssh.ClientConfig) (*ssh.Client, error) {
	address := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	conn, err := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, errors.New("No se pudo contactar con el servidor SSH. Revisa el host y el puerto")
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	sshConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		stop()
		conn.Close()
		return nil, err
	}
	client := ssh.NewClient(sshConn, channels, requests)
	// The request context remains active for the short enrollment operation.
	return client, nil
}

func (a *App) probeServerSSH(w http.ResponseWriter, r *http.Request) {
	var input enrollmentInput
	if decode(w, r, &input) != nil || input.Password != "" || input.Token != "" {
		fail(w, 400, "Datos de servidor inválidos")
		return
	}
	s := input.server()
	if err := validate(s); err != nil {
		fail(w, 400, err.Error())
		return
	}
	owner, err := a.session(r)
	if err != nil || owner == nil {
		fail(w, 401, "Inicia sesión de nuevo")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var hostKey []byte
	probeDone := errors.New("host identity captured")
	_, err = enrollmentClient(ctx, s, &ssh.ClientConfig{User: s.User, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		hostKey = append([]byte{}, key.Marshal()...)
		return probeDone
	}})
	if len(hostKey) == 0 {
		fail(w, 422, "No se pudo verificar la identidad SSH. Revisa el host, el puerto y que SSH esté habilitado")
		return
	}
	key, err := ssh.ParsePublicKey(hostKey)
	if err != nil {
		fail(w, 422, "El servidor no presentó una identidad SSH válida")
		return
	}
	token, err := randomToken()
	if err != nil {
		fail(w, 500, "No se pudo preparar la conexión")
		return
	}
	// Bounded, session-owned and short-lived; neither passwords nor client keys
	// are involved in this identity-only handshake.
	a.authMu.Lock()
	defer a.authMu.Unlock()
	_, err = a.db.Exec("DELETE FROM server_enrollments WHERE expires_at<=? OR owner=?", time.Now().Unix(), owner.Hash)
	var count int
	if err == nil {
		err = a.db.QueryRow("SELECT COUNT(*) FROM server_enrollments").Scan(&count)
	}
	if err != nil {
		fail(w, 503, "No se pudo preparar la conexión")
		return
	}
	if count >= 64 {
		authBusy(w)
		return
	}
	expires := time.Now().Add(10 * time.Minute)
	_, err = a.db.Exec("INSERT INTO server_enrollments VALUES(?,?,?,?,?)", tokenHash(token), owner.Hash, enrollmentBinding(s), hostKey, expires.Unix())
	if err != nil {
		fail(w, 503, "No se pudo preparar la conexión")
		return
	}
	reply(w, 200, map[string]any{"token": token, "fingerprint": ssh.FingerprintSHA256(key), "expiresAt": expires.UTC().Format(time.RFC3339)})
}

const installManagedKey = `set -eu
umask 077
test -n "$HOME"
cd "$HOME"
test ! -L .ssh
if test -e .ssh; then test -d .ssh; fi
mkdir -p .ssh
chmod 700 .ssh
test ! -L .ssh/authorized_keys
if test -e .ssh/authorized_keys; then test -f .ssh/authorized_keys; fi
touch .ssh/authorized_keys
chmod 600 .ssh/authorized_keys
IFS= read -r nodes_key
case "$nodes_key" in 'no-agent-forwarding,no-port-forwarding,no-X11-forwarding ssh-ed25519 '*) ;; *) exit 1;; esac
if ! grep -qxF -- "$nodes_key" .ssh/authorized_keys; then
  printf '\n%s\n' "$nodes_key" >> .ssh/authorized_keys
fi
`

func (a *App) connectServerSSH(w http.ResponseWriter, r *http.Request) {
	var input enrollmentInput
	if decode(w, r, &input) != nil {
		fail(w, 400, "Datos de servidor inválidos")
		return
	}
	password := input.Password
	input.Password = ""
	defer func() { password = "" }()
	s := input.server()
	if err := validate(s); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !input.Confirmed || !validToken(input.Token) || password == "" || len(password) > 4096 {
		fail(w, 400, "Confirma la huella y proporciona la contraseña SSH actual")
		return
	}
	owner, err := a.session(r)
	if err != nil || owner == nil {
		fail(w, 401, "Inicia sesión de nuevo")
		return
	}
	var hostKey []byte
	// DELETE RETURNING atomically claims a single attempt; replay, another
	// browser, edited connection details and expired probes cannot authorize SSH.
	err = a.db.QueryRow("DELETE FROM server_enrollments WHERE token_hash=? AND owner=? AND connection_hash=? AND expires_at>? RETURNING host_key", tokenHash(input.Token), owner.Hash, enrollmentBinding(s), time.Now().Unix()).Scan(&hostKey)
	if err != nil {
		fail(w, 409, "La verificación caducó o ya se usó. Verifica el servidor otra vez")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	pin := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if !bytes.Equal(key.Marshal(), hostKey) {
			return errors.New("server identity changed")
		}
		return nil
	}
	client, err := enrollmentClient(ctx, s, &ssh.ClientConfig{User: s.User, Auth: []ssh.AuthMethod{ssh.Password(password)}, HostKeyCallback: pin})
	password = ""
	if err != nil {
		fail(w, 422, "No se pudo autenticar con SSH o la huella cambió. Revisa la contraseña y verifica el servidor otra vez")
		return
	}
	defer client.Close()
	seed := make([]byte, ed25519.SeedSize)
	if _, err = rand.Read(seed); err != nil {
		fail(w, 500, "No se pudo crear el acceso SSH")
		return
	}
	defer clear(seed)
	protected, err := protectSecret(seed)
	if err != nil {
		fail(w, 500, "No se pudo proteger la clave SSH")
		return
	}
	private := ed25519.NewKeyFromSeed(seed)
	defer clear(private)
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		fail(w, 500, "No se pudo crear el acceso SSH")
		return
	}
	remote, err := client.NewSession()
	if err != nil {
		fail(w, 422, "No se pudo preparar la autorización SSH")
		return
	}
	defer remote.Close()
	publicLine := "no-agent-forwarding,no-port-forwarding,no-X11-forwarding " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " nodes-managed\n"
	remote.Stdin = strings.NewReader(publicLine)
	remote.Stdout, remote.Stderr = io.Discard, io.Discard
	if err = remote.Run(installManagedKey); err != nil {
		fail(w, 422, "No se pudo instalar el acceso SSH. Revisa los permisos de la cuenta y verifica el servidor otra vez")
		return
	}
	verification, err := enrollmentClient(ctx, s, &ssh.ClientConfig{User: s.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: pin})
	if err != nil {
		fail(w, 422, "El servidor no aceptó la nueva clave SSH. Revisa que permita autenticación por clave")
		return
	}
	verification.Close()
	// Logout during enrollment must prevent adding a server to a revoked session.
	if current, err := a.session(r); err != nil || current == nil {
		fail(w, 401, "La sesión terminó. Inicia sesión y conecta el servidor otra vez")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 503, "No se pudo guardar la conexión")
		return
	}
	defer tx.Rollback()
	result, err := tx.Exec("INSERT INTO servers(name,host,port,user,identity) VALUES(?,?,?,?,?)", s.Name, s.Host, s.Port, s.User, "")
	if err == nil {
		s.ID, err = result.LastInsertId()
	}
	if err == nil {
		_, err = tx.Exec("INSERT INTO server_ssh_keys VALUES(?,?,?)", s.ID, protected, hostKey)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 503, "No se pudo guardar la conexión. Verifica el servidor otra vez")
		return
	}
	s.ManagedSSH = true
	reply(w, 201, s)
}
