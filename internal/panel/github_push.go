package panel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/ssh"
)

type githubSecretsKey struct {
	ID  string `json:"key_id"`
	Key string `json:"key"`
}

func (a *App) githubSecretsKey(ctx context.Context, token []byte, d githubDeployment) (githubSecretsKey, error) {
	var key githubSecretsKey
	if err := a.githubAPI(ctx, token, "GET", "/repos/"+d.Repository+"/actions/secrets/public-key", nil, &key); err != nil {
		return key, errors.New("No se pudieron preparar los secretos. El token de GitHub necesita acceso a este repositorio y permiso Secrets: lectura y escritura. Actualiza el token en Nodes y reintenta")
	}
	decoded, err := base64.StdEncoding.DecodeString(key.Key)
	if err != nil || len(decoded) != 32 || key.ID == "" {
		return key, errors.New("GitHub devolvió una clave de cifrado inválida")
	}
	return key, nil
}

func encryptGithubSecret(key githubSecretsKey, value []byte) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(key.Key)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("Clave GitHub inválida")
	}
	var public [32]byte
	copy(public[:], decoded)
	encrypted, err := box.SealAnonymous(nil, value, &public, rand.Reader)
	if err != nil {
		return "", errors.New("No se pudo cifrar el secreto")
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func (a *App) githubPushPlan(w http.ResponseWriter, r *http.Request) {
	d, err := a.githubDeployment(r)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	token, err := a.githubToken()
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	defer clear(token)
	if _, err = a.githubSecretsKey(r.Context(), token, d); err != nil {
		fail(w, 400, err.Error())
		return
	}
	s, err := a.serverByID(d.ServerID)
	if err != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	p := &operationPlan{Kind: "github-push", Name: d.Name, Source: s.Name, Manager: "GitHub Actions", server: s,
		Summary:  []string{"Activar despliegue automático para " + d.Repository + " · rama " + d.Branch, "Se generará una clave SSH exclusiva y limitada al ejecutor de esta aplicación.", "Se configurarán los cinco secretos de Actions y se publicará el workflow."},
		Warnings: []string{"La clave exclusiva se guardará cifrada en GitHub Secrets; tu clave SSH personal no se subirá.", "Quien pueda ejecutar workflows con estos secretos podrá desplegar código en este servidor. Solo concede escritura a personas de confianza."}, Blockers: []string{}, Workflow: githubWorkflow(d), github: &githubDeploymentPlan{deployment: d}}
	a.savePlan(w, r, p)
}

// Only the fixed deployer and a full commit SHA are allowed through this key.
const githubSSHGate = `import os, pathlib, re, shlex, sys
base = pathlib.Path(__file__).resolve().parent
try:
 args = shlex.split(os.environ.get('SSH_ORIGINAL_COMMAND', ''))
 expected = '$HOME/.local/share/nodo/github/' + base.name + '/deploy.py'
 if len(args) != 3 or args[0] != 'python3' or args[1] not in (expected, str(base / 'deploy.py')) or not re.fullmatch('[a-f0-9]{40}', args[2]):
  raise ValueError()
except Exception:
 print('Esta clave solo permite desplegar la aplicación de Nodes.', file=sys.stderr)
 sys.exit(1)
os.execv(sys.executable, [sys.executable, str(base / 'deploy.py'), args[2]])
`

// Payloads contain public keys only. Never send the private key to the VPS.
const githubAuthorizeScript = `import fcntl, json, os, pathlib, sys, tempfile
os.umask(0o077)
base = pathlib.Path.home() / '.local/share/nodo/github' / sys.argv[1]
data = json.load(sys.stdin)
if json.loads((base / 'policy.json').read_text())['repository'] != data['repository']: raise ValueError('Repository changed')
directory = pathlib.Path.home() / '.ssh'
directory.mkdir(mode=0o700, exist_ok=True)
target = directory / 'authorized_keys'
with (directory / 'nodo-actions.lock').open('a') as lock:
 fcntl.flock(lock, fcntl.LOCK_EX)
 lines = target.read_text().splitlines() if target.exists() else []
 mode = sys.argv[2]
 if mode == 'add':
  gate = base / 'ssh_gate.py'
  gate_next = base / 'ssh_gate.next'
  gate_next.write_text(data['gate'])
  os.chmod(gate_next, 0o600)
  gate_next.replace(gate)
  lines.append(data['line'])
 elif mode == 'remove':
  lines = [line for line in lines if not line.endswith(' ' + data['marker'])]
 elif mode == 'retain':
  prefix = ' nodo-actions-' + sys.argv[1] + '-'
  lines = [line for line in lines if prefix not in line or line.endswith(' ' + data['marker'])]
 else: raise ValueError('Invalid mode')
 with tempfile.NamedTemporaryFile(mode='w',dir=directory,delete=False) as output:
  temporary = pathlib.Path(output.name)
  output.write('\n'.join(lines) + '\n')
 try:
  os.chmod(temporary,0o600)
  temporary.replace(target)
 finally:
  temporary.unlink(missing_ok=True)
`

func (a *App) authorizeGithubKey(ctx context.Context, s Server, d githubDeployment, mode string, payload map[string]string) error {
	raw, _ := json.Marshal(payload)
	command := "python3 -c " + shellQuote(githubAuthorizeScript) + " " + shellQuote(d.ID) + " " + shellQuote(mode)
	return a.stream(ctx, s, command, bytes.NewReader(raw), io.Discard)
}

func (a *App) configureGithubPush(ctx context.Context, p *operationPlan, token []byte) error {
	d := p.github.deployment
	if !terminalIDPattern.MatchString(d.ID) {
		return errors.New("Identificador de despliegue inválido")
	}
	key, err := a.githubSecretsKey(ctx, token, d)
	if err != nil {
		return err
	}
	host := sshPublicIP(ctx, p.server)
	if host == "" {
		return errors.New("No se pudo detectar una IP pública accesible desde GitHub Actions. Revisa la conexión SSH")
	}
	// The existing SSH channel already verifies the host. Read only public host keys.
	publicKeys, err := a.run(ctx, p.server, `for key in /etc/ssh/ssh_host_*_key.pub; do if test -r "$key"; then cat -- "$key"; fi; done`)
	if err != nil {
		return errors.New("No se pudieron leer las claves públicas del servidor desde la conexión SSH verificada")
	}
	target := host
	if p.server.Port != 22 {
		target = "[" + host + "]:" + strconv.Itoa(p.server.Port)
	}
	known := []string{}
	for _, line := range strings.Split(publicKeys, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields[:2], " "))); err != nil {
			continue
		}
		known = append(known, target+" "+strings.Join(fields[:2], " "))
	}
	if len(known) == 0 {
		return errors.New("No se encontraron claves públicas SSH compatibles; completa la huella manualmente")
	}
	home, err := a.run(ctx, p.server, `printf '%s' "$HOME"`)
	if err != nil || !serverFile(home) {
		return errors.New("No se pudo identificar la carpeta SSH del usuario remoto")
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.New("No se pudo crear la clave exclusiva")
	}
	defer clear(private)
	block, err := ssh.MarshalPrivateKey(private, "Nodes GitHub "+d.ID)
	if err != nil {
		return errors.New("No se pudo preparar la clave exclusiva")
	}
	defer clear(block.Bytes)
	privatePEM := pem.EncodeToMemory(block)
	defer clear(privatePEM)
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		return errors.New("No se pudo preparar la clave pública")
	}
	generation, err := randomToken()
	if err != nil {
		return err
	}
	marker := "nodo-actions-" + d.ID + "-" + generation[:16]
	gate := shellQuote(home + "/.local/share/nodo/github/" + d.ID + "/ssh_gate.py")
	forced := strings.ReplaceAll(strings.ReplaceAll("python3 "+gate, "\\", "\\\\"), "\"", "\\\"")
	line := "restrict,command=\"" + forced + "\" " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))) + " " + marker
	payload := map[string]string{"repository": d.Repository, "marker": marker, "gate": githubSSHGate, "line": line}
	a.operationState(p, "running", "Autorizando una clave exclusiva para el despliegue automático…")
	if err = a.authorizeGithubKey(ctx, p.server, d, "add", payload); err != nil {
		return errors.New("No se pudo autorizar la clave exclusiva en el servidor")
	}
	complete := false
	keyMayBePublished := false
	defer func() {
		if !complete && !keyMayBePublished {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = a.authorizeGithubKey(cleanup, p.server, d, "remove", payload)
		}
	}()
	values := []struct {
		suffix string
		value  []byte
	}{
		{"HOST", []byte(host)}, {"PORT", []byte(strconv.Itoa(p.server.Port))}, {"USER", []byte(p.server.User)}, {"KNOWN_HOSTS", []byte(strings.Join(known, "\n") + "\n")}, {"SSH_KEY", privatePEM},
	}
	a.operationState(p, "running", "Guardando los secretos cifrados en GitHub Actions…")
	for _, value := range values {
		encrypted, err := encryptGithubSecret(key, value.value)
		if err != nil {
			return err
		}
		// If the last request loses its response, GitHub may already hold this
		// key. Keep it authorized until a successful retry rotates it away.
		if value.suffix == "SSH_KEY" {
			keyMayBePublished = true
		}
		if err = a.githubAPI(ctx, token, "PUT", "/repos/"+d.Repository+"/actions/secrets/"+d.SecretPrefix+"_"+value.suffix, map[string]string{"encrypted_value": encrypted, "key_id": key.ID}, nil); err != nil {
			return fmt.Errorf("No se pudo guardar el secreto %s. Concede al token permiso Secrets: lectura y escritura y vuelve a activar el despliegue automático", value.suffix)
		}
	}
	complete = true
	if err = a.authorizeGithubKey(ctx, p.server, d, "retain", payload); err != nil {
		return errors.New("Los secretos están configurados, pero no se pudieron retirar claves anteriores. Vuelve a activar para completar la limpieza")
	}
	return nil
}

func (a *App) runGithubPush(ctx context.Context, p *operationPlan) error {
	token, err := a.githubToken()
	if err != nil {
		return err
	}
	defer clear(token)
	if err = a.configureGithubPush(ctx, p, token); err != nil {
		return err
	}
	d := p.github.deployment
	a.operationState(p, "running", "Publicando el workflow de GitHub Actions…")
	if err = a.publishGithubWorkflow(ctx, token, d); err != nil {
		return err
	}
	d.PushConfigured = true
	raw, _ := json.Marshal(d)
	if _, err = a.db.Exec("UPDATE github_deployments SET details=? WHERE id=?", string(raw), d.ID); err != nil {
		return errors.New("El workflow está publicado, pero no se pudo guardar su estado en Nodes")
	}
	return nil
}
