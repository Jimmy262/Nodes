package panel

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed github_deploy.py
var githubDeployer []byte

var githubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)
var githubBranchPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]{0,199}$`)
var githubBinding = Server{Host: "api.github.com", User: "github-token"}

type githubDeployment struct {
	ID           string `json:"id"`
	ServerID     int64  `json:"serverId"`
	Name         string `json:"name"`
	Repository   string `json:"repository"`
	Branch       string `json:"branch"`
	BuildPath    string `json:"buildPath"`
	Project      string `json:"project"`
	Workflow     string `json:"workflow"`
	SecretPrefix string `json:"secretPrefix"`
	URL          string `json:"url,omitempty"`
}
type githubDeploymentPlan struct {
	deployment githubDeployment
	policy     []byte
	commit     string
}

func (a *App) githubToken() ([]byte, error) {
	var protected, encrypted []byte
	if err := a.db.QueryRow("SELECT protected_key,secret FROM github_connection WHERE id=1").Scan(&protected, &encrypted); err != nil {
		return nil, errors.New("Conecta GitHub en Nodo para continuar")
	}
	token, err := openConnectionSecret(githubBinding, protected, encrypted)
	if err != nil {
		return nil, errors.New("No se pudo abrir el token; vuelve a conectar GitHub")
	}
	return token, nil
}

// Never follow an API redirect with credentials. Archive redirects are fetched
// separately, only from GitHub's codeload host and without Authorization.
func (a *App) githubRequest(ctx context.Context, token []byte, method, endpoint string, body any) (*http.Response, error) {
	if !strings.HasPrefix(endpoint, "/") || strings.ContainsAny(endpoint, "\r\n#") {
		return nil, errors.New("Ruta GitHub inválida")
	}
	var reader io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		reader = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, "https://api.github.com"+endpoint, reader)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Nodo")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := a.githubClient()
	response, e := client.Do(req)
	if e != nil {
		return nil, errors.New("No se pudo conectar con GitHub")
	}
	return response, nil
}
func (a *App) githubClient() *http.Client {
	client := http.Client{Timeout: 90 * time.Second}
	if a.githubHTTP != nil {
		client = *a.githubHTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}
func githubError(status int) error {
	switch status {
	case 401:
		return errors.New("GitHub rechazó el token. Vuelve a conectar la cuenta")
	case 403, 429:
		return errors.New("GitHub rechazó los permisos o alcanzó su límite de solicitudes. Revisa el token y la autorización de la organización")
	case 404:
		return errors.New("GitHub no encontró el repositorio, rama o workflow. Revisa el nombre y los permisos del token")
	case 409, 422:
		return errors.New("GitHub no pudo guardar o ejecutar el workflow. Revisa la rama, las reglas del repositorio y que el workflow esté en la rama predeterminada para ejecución manual")
	default:
		return errors.New("GitHub no completó la operación; revisa Actions antes de reintentar")
	}
}
func (a *App) githubAPI(ctx context.Context, token []byte, method, endpoint string, body, out any) error {
	response, e := a.githubRequest(ctx, token, method, endpoint, body)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return githubError(response.StatusCode)
	}
	if out != nil {
		b, e := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
		if e != nil || len(b) > 4<<20 || json.Unmarshal(b, out) != nil {
			return errors.New("Respuesta GitHub inválida o demasiado grande")
		}
	}
	return nil
}
func (a *App) githubStatus(w http.ResponseWriter, r *http.Request) {
	var login string
	if a.db.QueryRow("SELECT login FROM github_connection WHERE id=1").Scan(&login) != nil {
		reply(w, 200, map[string]any{"connected": false})
		return
	}
	reply(w, 200, map[string]any{"connected": true, "login": login})
}
func (a *App) connectGithub(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if decode(w, r, &in) != nil || len(in.Token) < 16 || len(in.Token) > 2048 {
		fail(w, 400, "Pega un token personal de GitHub válido")
		return
	}
	for _, ch := range in.Token {
		if ch < 33 || ch > 126 {
			fail(w, 400, "Token inválido")
			return
		}
	}
	token := []byte(in.Token)
	defer clear(token)
	var user struct {
		Login string `json:"login"`
	}
	if e := a.githubAPI(r.Context(), token, "GET", "/user", nil, &user); e != nil {
		fail(w, 502, e.Error())
		return
	}
	if user.Login == "" {
		fail(w, 502, "GitHub no devolvió una cuenta válida")
		return
	}
	protected, encrypted, e := sealDokployKey(githubBinding, token)
	if e != nil {
		fail(w, 500, "Windows no pudo proteger el token; no se guardó")
		return
	}
	if _, e = a.db.Exec("INSERT INTO github_connection VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET login=excluded.login,protected_key=excluded.protected_key,secret=excluded.secret", user.Login, protected, encrypted); e != nil {
		fail(w, 500, "No se pudo guardar la conexión")
		return
	}
	reply(w, 200, map[string]any{"connected": true, "login": user.Login})
}
func (a *App) disconnectGithub(w http.ResponseWriter, r *http.Request) {
	if _, e := a.db.Exec("DELETE FROM github_connection"); e != nil {
		fail(w, 500, "No se pudo desconectar GitHub")
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
func (a *App) githubRepositories(w http.ResponseWriter, r *http.Request) {
	token, e := a.githubToken()
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	defer clear(token)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 || page > 100 {
		page = 1
	}
	var raw []struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
	}
	e = a.githubAPI(r.Context(), token, "GET", fmt.Sprintf("/user/repos?sort=updated&per_page=100&page=%d", page), nil, &raw)
	if e != nil {
		fail(w, 502, e.Error())
		return
	}
	// Send a small allowlist rather than GitHub's complete repository objects.
	reply(w, 200, map[string]any{"items": raw, "hasMore": len(raw) == 100, "page": page})
}

func (a *App) githubArchive(ctx context.Context, token []byte, repo, branch, buildPath string) (*zipProject, error) {
	var commit struct {
		SHA string `json:"sha"`
	}
	if e := a.githubAPI(ctx, token, "GET", "/repos/"+repo+"/commits/"+url.PathEscape(branch), nil, &commit); e != nil {
		return nil, e
	}
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(commit.SHA) {
		return nil, errors.New("Commit GitHub inválido")
	}
	res, e := a.githubRequest(ctx, token, "GET", "/repos/"+repo+"/zipball/"+commit.SHA, nil)
	if e != nil {
		return nil, e
	}
	if res.StatusCode == 302 {
		location, err := res.Location()
		res.Body.Close()
		if err != nil || location.Scheme != "https" || location.Host != "codeload.github.com" || location.User != nil {
			return nil, errors.New("Redirección GitHub no permitida")
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", location.String(), nil)
		res, e = a.githubClient().Do(req)
		if e != nil {
			return nil, errors.New("No se pudo descargar el repositorio")
		}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, githubError(res.StatusCode)
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, maxZipSize+1))
	if e != nil || len(data) > maxZipSize {
		return nil, errors.New("El repositorio supera 64 MiB o no se pudo descargar")
	}
	data, e = githubSubdirectory(data, buildPath)
	if e != nil {
		return nil, e
	}
	return inspectZip(data, 8080, 80)
}
func githubSubdirectory(data []byte, subdir string) ([]byte, error) {
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil || len(z.File) == 0 || len(z.File) > 6000 {
		return nil, errors.New("Archivo GitHub inválido")
	}
	prefix := strings.SplitN(z.File[0].Name, "/", 2)[0] + "/"
	if subdir != "." {
		prefix += subdir + "/"
	}
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	var total uint64
	for _, f := range z.File {
		if !safeArchivePath(f.Name) {
			return nil, errors.New("El archivo contiene rutas inseguras")
		}
		if !strings.HasPrefix(f.Name, prefix) || f.FileInfo().IsDir() {
			continue
		}
		name := strings.TrimPrefix(f.Name, prefix)
		if !safeArchivePath(name) || !f.Mode().IsRegular() {
			return nil, errors.New("El repositorio contiene enlaces o rutas no admitidas")
		}
		total += f.UncompressedSize64
		if total > maxExpandedSize || f.UncompressedSize64 > maxZipSize {
			return nil, errors.New("El repositorio supera los límites de tamaño")
		}
		header := f.FileHeader
		header.Name = name
		dst, e := writer.CreateHeader(&header)
		if e != nil {
			return nil, e
		}
		src, e := f.Open()
		if e != nil {
			return nil, e
		}
		_, e = io.Copy(dst, io.LimitReader(src, int64(f.UncompressedSize64)+1))
		src.Close()
		if e != nil {
			return nil, e
		}
	}
	if e = writer.Close(); e != nil {
		return nil, e
	}
	return out.Bytes(), nil
}

func (a *App) githubPlan(w http.ResponseWriter, r *http.Request) {
	s, e := a.server(r)
	if e != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	var in struct {
		Name        string     `json:"name"`
		Repository  string     `json:"repository"`
		Branch      string     `json:"branch"`
		BuildPath   string     `json:"buildPath"`
		Access      string     `json:"access"`
		Domain      string     `json:"domain"`
		Service     string     `json:"service"`
		Port        int        `json:"port"`
		HostPort    int        `json:"hostPort"`
		HTTPS       bool       `json:"https"`
		Environment []envValue `json:"environment"`
	}
	// Environment values can legitimately exceed the general 8 KiB request limit.
	r.Body = http.MaxBytesReader(w, r.Body, 80<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		fail(w, 400, "Configuración inválida")
		return
	}
	if in.BuildPath == "" {
		in.BuildPath = "."
	}
	if !projectPattern.MatchString(in.Name) || !githubRepositoryPattern.MatchString(in.Repository) || !githubBranchPattern.MatchString(in.Branch) || strings.Contains(in.Branch, "..") || strings.Contains(in.Branch, "//") || strings.HasSuffix(in.Branch, "/") || strings.HasSuffix(in.Branch, ".lock") || !relativeProjectPath(in.BuildPath) {
		fail(w, 400, "Revisa el nombre, repositorio, rama y carpeta del proyecto")
		return
	}
	in.BuildPath = path.Clean(in.BuildPath)
	if in.Access != "port" && in.Access != "auto" && in.Access != "custom" {
		fail(w, 400, "Escoge el tipo de acceso")
		return
	}
	if in.Port < 1 || in.Port > 65535 || in.HostPort < 1 || in.HostPort > 65535 {
		fail(w, 400, "Los puertos deben estar entre 1 y 65535")
		return
	}
	envRaw, _ := json.Marshal(in.Environment)
	env, e := validateEnvironment(string(envRaw))
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	token, e := a.githubToken()
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	defer clear(token)
	z, e := a.githubArchive(r.Context(), token, in.Repository, in.Branch, in.BuildPath)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	if in.Service == "" && len(z.services) == 1 {
		in.Service = z.services[0]
	}
	if z.config["services"].(map[string]any)[in.Service] == nil {
		fail(w, 400, "Indica el servicio público del Compose: "+strings.Join(z.services, ", "))
		return
	}
	id, e := randomToken()
	if e != nil {
		fail(w, 500, "No se pudo preparar el despliegue")
		return
	}
	deployment := githubDeployment{ID: id, ServerID: s.ID, Name: in.Name, Repository: in.Repository, Branch: in.Branch, BuildPath: in.BuildPath, Project: in.Name + "-" + id[:8], Workflow: "nodo-" + id[:12] + ".yml", SecretPrefix: "NODO_" + strings.ToUpper(id[:8])}
	p := &operationPlan{ID: id, Kind: "github-setup", Name: in.Name, Source: s.Name, Manager: "GitHub Actions", server: s, project: deployment.Project, Summary: []string{"Repositorio: " + in.Repository + " · rama " + in.Branch, "Se instalará el ejecutor de Nodo en la VPS y se publicará .github/workflows/" + deployment.Workflow, "Cada push desplegará este proyecto, conservando su nombre y sus volúmenes Docker."}, Warnings: []string{"El código de esta rama se ejecutará en tu servidor. Solo concede escritura en el repositorio a personas de confianza.", "Necesitarás configurar los secretos SSH en GitHub. Publicar el workflow puede iniciar un primer intento antes de configurar esos secretos; se puede volver a ejecutar después.", "Los cambios futuros del Compose también se aplicarán. Un fallo puede dejar servicios parcialmente actualizados; no se revierte ni se borran volúmenes automáticamente."}, Blockers: []string{}}
	if e = a.prepareAccess(r.Context(), p, in.Access, in.Domain, in.Service, in.Port, in.HTTPS); e != nil {
		fail(w, 400, e.Error())
		return
	}
	deployment.URL = p.URL
	// Derive only routing and environment policy. Every future push reads its own
	// Compose definition instead of silently reusing a stale snapshot.
	if e = configureDeployment(z, p, env, in.Service); e != nil {
		fail(w, 400, e.Error())
		return
	}
	labels := map[string]any{}
	for _, name := range z.services {
		routing := map[string]any{}
		for key, value := range z.config["services"].(map[string]any)[name].(map[string]any)["labels"].(map[string]any) {
			if strings.HasPrefix(key, "traefik.") {
				routing[key] = value
			}
		}
		labels[name] = routing
	}
	composeName := path.Base(z.compose)
	policy := map[string]any{"project": deployment.Project, "repository": in.Repository, "branch": in.Branch, "buildPath": in.BuildPath, "compose": composeName, "dockerfile": z.dockerfile, "service": in.Service, "port": in.Port, "hostPort": in.HostPort, "environment": env, "labels": labels, "proxy": p.access != nil}
	if p.access != nil {
		policy["network"] = p.access.Proxy.Network
	}
	raw, _ := json.Marshal(policy)
	p.github = &githubDeploymentPlan{deployment: deployment, policy: raw}
	p.Workflow = githubWorkflow(deployment)
	if _, e = a.run(r.Context(), s, `docker info >/dev/null && docker compose version >/dev/null && command -v tar >/dev/null && python3 -c 'import sys, fcntl, tarfile, json, pathlib; assert sys.version_info >= (3, 10)' && docker compose config --help | grep -q -- --no-path-resolution && docker compose up --help | grep -q -- --wait-timeout`); e != nil {
		p.Blockers = append(p.Blockers, "La VPS necesita Docker Compose v2 reciente, tar y Python 3.10 o posterior accesibles por SSH.")
	}
	a.savePlan(w, r, p)
}

func (a *App) runGithubSetup(ctx context.Context, p *operationPlan) error {
	if e := a.recheckAccess(ctx, p); e != nil {
		return e
	}
	g := p.github
	d := g.deployment
	token, e := a.githubToken()
	if e != nil {
		return e
	}
	defer clear(token)
	a.operationState(p, "running", "Instalando el ejecutor de Nodo y su configuración privada en la VPS…")
	var upload bytes.Buffer
	tw := tar.NewWriter(&upload)
	for _, f := range []struct {
		name string
		data []byte
	}{{"deploy.py", githubDeployer}, {"policy.json", g.policy}} {
		if e = tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0600, Size: int64(len(f.data))}); e != nil {
			return e
		}
		if _, e = tw.Write(f.data); e != nil {
			return e
		}
	}
	if e = tw.Close(); e != nil {
		return e
	}
	command := `set -eu; umask 077; mkdir -p "$HOME/.local/share/nodo/github"; mkdir "$HOME/.local/share/nodo/github/` + d.ID + `"; tar -xf - -C "$HOME/.local/share/nodo/github/` + d.ID + `"`
	if e = a.stream(ctx, p.server, command, &upload, io.Discard); e != nil {
		return errors.New("No se pudo instalar el ejecutor. Puede quedar una carpeta privada de preparación en la VPS")
	}
	raw, _ := json.Marshal(d)
	if _, e = a.db.Exec("INSERT INTO github_deployments VALUES(?,?,?,?)", d.ID, d.ServerID, connectionHash(p.server), string(raw)); e != nil {
		return errors.New("El ejecutor se instaló, pero no se pudo guardar la conexión local")
	}
	a.operationState(p, "running", "Publicando el workflow en la rama seleccionada de GitHub…")
	if e = a.publishGithubWorkflow(ctx, token, d); e != nil {
		return fmt.Errorf("La VPS está preparada. %s. Puedes reintentar Publicar workflow en GitHub desde Nodo", e)
	}
	return nil
}
func (a *App) githubDeployment(r *http.Request) (githubDeployment, error) {
	var d githubDeployment
	var raw, binding string
	var sid int64
	e := a.db.QueryRow("SELECT server_id,connection_hash,details FROM github_deployments WHERE id=?", r.PathValue("deployment")).Scan(&sid, &binding, &raw)
	if e != nil {
		return d, errors.New("Despliegue no encontrado")
	}
	s, e := a.serverByID(sid)
	if e != nil || binding != connectionHash(s) {
		return d, errors.New("La conexión SSH cambió o se retiró. Prepara otro despliegue")
	}
	if json.Unmarshal([]byte(raw), &d) != nil {
		return d, errors.New("Registro de despliegue inválido")
	}
	return d, nil
}
func (a *App) githubDeployments(w http.ResponseWriter, r *http.Request) {
	rows, e := a.db.Query("SELECT details FROM github_deployments WHERE server_id IN (SELECT id FROM servers) ORDER BY rowid DESC")
	if e != nil {
		fail(w, 500, "No se pudieron leer los despliegues")
		return
	}
	defer rows.Close()
	out := []githubDeployment{}
	for rows.Next() {
		var raw string
		var d githubDeployment
		if rows.Scan(&raw) != nil || json.Unmarshal([]byte(raw), &d) != nil {
			fail(w, 500, "Registro inválido")
			return
		}
		out = append(out, d)
	}
	if rows.Err() != nil {
		fail(w, 500, "No se pudieron leer los despliegues")
		return
	}
	reply(w, 200, out)
}
func (a *App) publishGithubWorkflow(ctx context.Context, token []byte, d githubDeployment) error {
	content := githubWorkflow(d)
	endpoint := "/repos/" + d.Repository + "/contents/.github/workflows/" + d.Workflow
	response, e := a.githubRequest(ctx, token, "GET", endpoint+"?ref="+url.QueryEscape(d.Branch), nil)
	if e != nil {
		return e
	}
	status := response.StatusCode
	if status == 200 {
		var existing struct {
			Content string `json:"content"`
		}
		e = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&existing)
		response.Body.Close()
		decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(existing.Content, "\n", ""))
		if e == nil && err == nil && string(decoded) == content {
			return nil
		}
		return errors.New("El workflow ya existe y fue modificado; no se sobrescribió")
	}
	response.Body.Close()
	if status != 404 {
		return githubError(status)
	}
	return a.githubAPI(ctx, token, "PUT", endpoint, map[string]any{"message": "Configure Nodo deployment for " + d.Name, "branch": d.Branch, "content": base64.StdEncoding.EncodeToString([]byte(content))}, nil)
}
func (a *App) githubPublish(w http.ResponseWriter, r *http.Request) {
	d, e := a.githubDeployment(r)
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	token, e := a.githubToken()
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	defer clear(token)
	if e = a.publishGithubWorkflow(r.Context(), token, d); e != nil {
		fail(w, 502, e.Error())
		return
	}
	reply(w, 200, map[string]bool{"published": true})
}
func (a *App) githubDispatch(w http.ResponseWriter, r *http.Request) {
	d, e := a.githubDeployment(r)
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	token, e := a.githubToken()
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	defer clear(token)
	var commit struct {
		SHA string `json:"sha"`
	}
	if e = a.githubAPI(r.Context(), token, "GET", "/repos/"+d.Repository+"/commits/"+url.PathEscape(d.Branch), nil, &commit); e != nil {
		fail(w, 502, e.Error())
		return
	}
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(commit.SHA) {
		fail(w, 502, "Commit GitHub inválido")
		return
	}
	s, e := a.serverByID(d.ServerID)
	if e != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	p := &operationPlan{Kind: "github-deploy", Name: d.Name, Source: s.Name, Manager: "GitHub · Nodo", server: s, URL: d.URL, Summary: []string{"Repositorio: " + d.Repository + " · rama " + d.Branch, "Commit: " + commit.SHA, "Se reconstruirán los servicios modificados en el proyecto " + d.Project + ". Se conservan sus volúmenes."}, Warnings: []string{"Los servicios pueden interrumpirse mientras se actualizan. Si falla, revisa el resultado antes de reintentar.", "Mantén Nodo abierto hasta que termine este despliegue manual."}, Blockers: []string{}, github: &githubDeploymentPlan{deployment: d, commit: commit.SHA}}
	a.savePlan(w, r, p)
}

func (a *App) runGithubDeploy(ctx context.Context, p *operationPlan) error {
	token, e := a.githubToken()
	if e != nil {
		return e
	}
	defer clear(token)
	g := p.github
	a.operationState(p, "running", "Descargando el commit "+g.commit+" desde GitHub…")
	response, e := a.githubRequest(ctx, token, "GET", "/repos/"+g.deployment.Repository+"/tarball/"+g.commit, nil)
	if e != nil {
		return e
	}
	if response.StatusCode == 302 {
		location, err := response.Location()
		response.Body.Close()
		if err != nil || location.Scheme != "https" || location.Host != "codeload.github.com" || location.User != nil {
			return errors.New("Redirección GitHub no permitida")
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", location.String(), nil)
		response, e = a.githubClient().Do(req)
		if e != nil {
			return errors.New("No se pudo descargar el commit")
		}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return githubError(response.StatusCode)
	}
	data, e := io.ReadAll(io.LimitReader(response.Body, maxZipSize+1))
	if e != nil || len(data) > maxZipSize {
		return errors.New("No se pudo descargar el commit o supera 64 MiB")
	}
	a.operationState(p, "running", "Subiendo el commit, construyendo y comprobando los servicios…")
	command := `python3 "$HOME/.local/share/nodo/github/` + g.deployment.ID + `/deploy.py" ` + g.commit
	if e = a.stream(ctx, p.server, command, bytes.NewReader(data), io.Discard); e != nil {
		return errors.New("No se confirmó el despliegue. Puede haber servicios parcialmente actualizados; revisa Aplicaciones y ~/.local/share/nodo/github/" + g.deployment.ID + "/releases/<release>/deploy.log en la VPS")
	}
	return nil
}
func (a *App) githubRuns(w http.ResponseWriter, r *http.Request) {
	d, e := a.githubDeployment(r)
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	token, e := a.githubToken()
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	defer clear(token)
	var raw struct {
		Runs []struct {
			ID         int64  `json:"id"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			SHA        string `json:"head_sha"`
			URL        string `json:"html_url"`
			Created    string `json:"created_at"`
		} `json:"workflow_runs"`
	}
	e = a.githubAPI(r.Context(), token, "GET", "/repos/"+d.Repository+"/actions/workflows/"+d.Workflow+"/runs?per_page=10&branch="+url.QueryEscape(d.Branch), nil, &raw)
	if e != nil {
		fail(w, 502, e.Error())
		return
	}
	if raw.Runs == nil {
		reply(w, 200, []any{})
		return
	}
	reply(w, 200, raw.Runs)
}

func githubWorkflow(d githubDeployment) string {
	// JSON scalars are valid YAML, including branch names and workflow titles.
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return `name: ` + q("Nodo · "+d.Name) + `
on:
  push:
    branches: [` + q(d.Branch) + `]
  workflow_dispatch:
permissions:
  contents: read
concurrency:
  group: nodo-` + d.ID + `
  cancel-in-progress: false
jobs:
  deploy:
    if: github.ref == 'refs/heads/` + d.Branch + `'
    runs-on: ubuntu-latest
    timeout-minutes: 30
    env:
      GH_TOKEN: ${{ github.token }}
      REPOSITORY: ${{ github.repository }}
      COMMIT: ${{ github.sha }}
      SSH_HOST: ${{ secrets.` + d.SecretPrefix + `_HOST }}
      SSH_PORT: ${{ secrets.` + d.SecretPrefix + `_PORT }}
      SSH_USER: ${{ secrets.` + d.SecretPrefix + `_USER }}
      SSH_KEY: ${{ secrets.` + d.SecretPrefix + `_SSH_KEY }}
      SSH_KNOWN_HOSTS: ${{ secrets.` + d.SecretPrefix + `_KNOWN_HOSTS }}
    steps:
      - name: Desplegar commit en la VPS
        shell: bash
        run: |
          set -euo pipefail
          test -n "$SSH_HOST" && test -n "$SSH_USER" && test -n "$SSH_KEY" && test -n "$SSH_KNOWN_HOSTS" || { echo "Configura los secretos SSH de Nodo en este repositorio"; exit 1; }
          [[ "$SSH_HOST" =~ ^[a-zA-Z0-9][a-zA-Z0-9.:-]*$ ]] && [[ "$SSH_USER" =~ ^[a-z_][a-z0-9_-]*$ ]] && [[ "${SSH_PORT:-22}" =~ ^[0-9]{1,5}$ ]]
          [[ "$COMMIT" =~ ^[a-f0-9]{40}$ ]]
          umask 077
          mkdir -p "$RUNNER_TEMP/nodo-ssh"
          trap 'rm -f "$RUNNER_TEMP/nodo-ssh/key" "$RUNNER_TEMP/nodo-ssh/known_hosts" "$RUNNER_TEMP/nodo-source.tar.gz"' EXIT
          printf '%s\n' "$SSH_KEY" > "$RUNNER_TEMP/nodo-ssh/key"
          printf '%s\n' "$SSH_KNOWN_HOSTS" > "$RUNNER_TEMP/nodo-ssh/known_hosts"
          curl --fail --silent --show-error --location --max-time 120 --max-filesize 67108864 \
            -H "Authorization: Bearer $GH_TOKEN" -H 'Accept: application/vnd.github+json' \
            "https://api.github.com/repos/$REPOSITORY/tarball/$COMMIT" -o "$RUNNER_TEMP/nodo-source.tar.gz"
          ssh -T -o BatchMode=yes -o StrictHostKeyChecking=yes -o IdentitiesOnly=yes \
            -o ForwardAgent=no -o ClearAllForwardings=yes -o ConnectTimeout=15 \
            -o ServerAliveInterval=15 -o ServerAliveCountMax=4 \
            -o "UserKnownHostsFile=$RUNNER_TEMP/nodo-ssh/known_hosts" \
            -i "$RUNNER_TEMP/nodo-ssh/key" -p "${SSH_PORT:-22}" -- "$SSH_USER@$SSH_HOST" \
            "python3 \"\$HOME/.local/share/nodo/github/` + d.ID + `/deploy.py\" $COMMIT" < "$RUNNER_TEMP/nodo-source.tar.gz"
`
}
