package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"

	"net"
	"net/http"

	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Server struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Identity string `json:"identity"`
}
type Sample struct {
	At           string  `json:"at"`
	ConnectionIP string  `json:"connectionIP,omitempty"`
	CPU          float64 `json:"cpu"`
	Memory       float64 `json:"memory"`
	Disk         float64 `json:"disk"`
	Uptime       float64 `json:"uptime"`
	RX           float64 `json:"rx"`
	TX           float64 `json:"tx"`
	Cores        float64 `json:"cores,omitempty"`
	MemoryTotal  float64 `json:"memoryTotal,omitempty"`
	MemoryUsed   float64 `json:"memoryUsed,omitempty"`
	DiskTotal    float64 `json:"diskTotal,omitempty"`
	DiskUsed     float64 `json:"diskUsed,omitempty"`
}
type State struct {
	Status string  `json:"status"`
	Error  string  `json:"error,omitempty"`
	Sample *Sample `json:"sample,omitempty"`
}
type App struct {
	db               *sql.DB
	setupToken       string
	setupUntil       time.Time
	authMu           sync.Mutex
	mu               sync.RWMutex
	states           map[int64]State
	docker           map[int64]DockerSnapshot
	run              func(context.Context, Server, string) (string, error)
	operations       *operationManager
	stream           func(context.Context, Server, string, io.Reader, io.Writer) error
	githubHTTP       *http.Client
	terminalContext  context.Context
	terminalCancel   context.CancelFunc
	terminalSlots    chan struct{}
	startTerminal    func(context.Context, Server, string) (terminalBackend, error)
	publicOrigin     string
	swarmDNS         func(context.Context, string, string) domainStatus
	preparationSlots chan struct{}
}

func New(path, setupToken string) (*App, error) {
	db, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	a := &App{db: db, setupToken: setupToken, states: map[int64]State{}, docker: map[int64]DockerSnapshot{}, run: runSSH, stream: streamSSH}
	a.operations = newOperationManager()
	a.preparationSlots = make(chan struct{}, 2)
	a.terminalContext, a.terminalCancel = context.WithCancel(context.Background())
	a.terminalSlots = make(chan struct{}, 8)
	return a, nil
}
func (a *App) Close() { a.terminalCancel(); a.operations.close(); a.db.Close() }
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("JSON inválido")
	}
	return nil
}
func (a *App) Handler(files fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/status", a.authStatus)
	mux.HandleFunc("POST /api/auth/prepare", a.prepareSetup)
	mux.HandleFunc("POST /api/auth/setup", a.setupPassword)
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("GET /api/session", a.authStatus)
	mux.HandleFunc("POST /api/logout", a.logout)
	mux.HandleFunc("POST /api/auth/password", a.changePassword)
	mux.HandleFunc("POST /api/auth/logout-all", a.logoutAll)
	mux.HandleFunc("GET /api/servers", a.list)
	mux.HandleFunc("GET /api/overview", a.overview)
	mux.HandleFunc("POST /api/servers", a.create)
	mux.HandleFunc("PATCH /api/servers/{id}", a.rename)
	mux.HandleFunc("DELETE /api/servers/{id}", a.remove)
	mux.HandleFunc("GET /api/servers/{id}/history", a.history)
	mux.HandleFunc("POST /api/servers/{id}/check", a.check)
	mux.HandleFunc("POST /api/servers/{id}/terminal", a.prepareTerminal)
	mux.HandleFunc("GET /api/servers/{id}/terminal/options", a.terminalOptions)
	mux.HandleFunc("POST /api/servers/{id}/terminal/install/plan", a.limitPreparation(a.terminalInstallReview))
	mux.HandleFunc("GET /api/servers/{id}/terminal/{terminal}", a.terminalSocket)
	mux.HandleFunc("GET /api/servers/{id}/containers", a.containers)
	mux.HandleFunc("GET /api/servers/{id}/containers/{container}/logs", a.logs)
	mux.HandleFunc("POST /api/servers/{id}/containers/{container}/plan", a.limitPreparation(a.containerPlan))
	mux.HandleFunc("POST /api/servers/{id}/deploy/plan", a.limitPreparation(a.deployPlan))
	mux.HandleFunc("GET /api/servers/{id}/deploy/options", a.deploymentOptions)
	mux.HandleFunc("POST /api/deploy/inspect", a.limitPreparation(a.inspectDeployment))
	mux.HandleFunc("GET /api/templates", a.composeTemplates)
	mux.HandleFunc("POST /api/templates/{template}/defaults", a.composeTemplateDefaults)
	mux.HandleFunc("POST /api/servers/{id}/templates/{template}/plan", a.limitPreparation(a.composeTemplatePlan))
	mux.HandleFunc("GET /api/servers/{id}/application/configuration", a.getApplicationConfiguration)
	mux.HandleFunc("POST /api/servers/{id}/application/plan", a.limitPreparation(a.applicationConfigurationPlan))
	mux.HandleFunc("POST /api/servers/{id}/dokploy/connect", a.connectDokploy)
	mux.HandleFunc("GET /api/github/connection", a.githubStatus)
	mux.HandleFunc("POST /api/github/connection", a.connectGithub)
	mux.HandleFunc("DELETE /api/github/connection", a.disconnectGithub)
	mux.HandleFunc("GET /api/github/repositories", a.githubRepositories)
	mux.HandleFunc("POST /api/servers/{id}/github/plan", a.limitPreparation(a.githubPlan))
	mux.HandleFunc("GET /api/github/deployments", a.githubDeployments)
	mux.HandleFunc("GET /api/github/deployments/{deployment}/runs", a.githubRuns)
	mux.HandleFunc("POST /api/github/deployments/{deployment}/deploy", a.githubDispatch)
	mux.HandleFunc("POST /api/github/deployments/{deployment}/publish", a.githubPublish)
	mux.HandleFunc("POST /api/operations/{operation}/execute", a.executeOperation)
	mux.HandleFunc("DELETE /api/operations/{operation}/plan", a.discardPlan)
	mux.HandleFunc("GET /api/operations", a.listOperations)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "Ruta no encontrada") })
	mux.Handle("/", publicFiles(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		host, _, err := net.SplitHostPort(r.Host)
		if !a.allowedHost(r, host, err) {
			fail(w, 403, "Host no permitido")
			return
		}
		if a.secureRequest(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		websocketOrigin := strings.Replace(strings.Replace(a.requestOrigin(r), "https://", "wss://", 1), "http://", "ws://", 1)
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' "+websocketOrigin+"; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			if r.Method != "GET" && r.Method != "HEAD" {
				origin := r.Header.Get("Origin")
				if origin != a.requestOrigin(r) {
					fail(w, 403, "Origen no permitido")
					return
				}
			}
			public := r.URL.Path == "/api/login" || r.URL.Path == "/api/logout" || r.URL.Path == "/api/auth/status" || r.URL.Path == "/api/auth/prepare" || r.URL.Path == "/api/auth/setup"
			if !public {
				session, err := a.session(r)
				if err != nil {
					fail(w, 503, "No se pudo comprobar la sesión")
					return
				}
				if session == nil {
					fail(w, 401, "Inicia sesión con tu PIN")
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *App) servers() ([]Server, error) {
	rows, err := a.db.Query("SELECT id,name,host,port,user,identity FROM servers ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Server{}
	for rows.Next() {
		var s Server
		if err := rows.Scan(&s.ID, &s.Name, &s.Host, &s.Port, &s.User, &s.Identity); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (a *App) server(r *http.Request) (Server, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return Server{}, err
	}
	var s Server
	err = a.db.QueryRow("SELECT id,name,host,port,user,identity FROM servers WHERE id=?", id).Scan(&s.ID, &s.Name, &s.Host, &s.Port, &s.User, &s.Identity)
	return s, err
}
func (a *App) list(w http.ResponseWriter, r *http.Request) {
	servers, err := a.servers()
	if err != nil {
		fail(w, 500, "No se pudo leer el inventario")
		return
	}
	type row struct {
		Server
		State State `json:"state"`
	}
	out := []row{}
	a.mu.RLock()
	for _, s := range servers {
		state, ok := a.states[s.ID]
		if !ok {
			state = State{Status: "pending"}
		}
		out = append(out, row{s, state})
	}
	a.mu.RUnlock()
	reply(w, 200, out)
}

var hostPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.:-]{0,252}$`)
var userPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,63}$`)

func validate(s Server) error {
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > 80 {
		return errors.New("Indica un nombre de hasta 80 caracteres")
	}
	if !hostPattern.MatchString(s.Host) {
		return errors.New("Host inválido: usa una IP o nombre de dominio")
	}
	if !userPattern.MatchString(s.User) {
		return errors.New("Usuario SSH inválido")
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("Puerto inválido")
	}
	if len(s.Identity) > 512 || strings.ContainsAny(s.Identity, "\r\n\x00") {
		return errors.New("Ruta de clave inválida")
	}
	if s.Identity != "" && !filepath.IsAbs(s.Identity) {
		return errors.New("La ruta de la clave debe ser absoluta")
	}
	return nil
}
func (a *App) create(w http.ResponseWriter, r *http.Request) {
	var s Server
	if decode(w, r, &s) != nil {
		fail(w, 400, "Datos inválidos")
		return
	}
	s.Name = strings.TrimSpace(s.Name)
	s.Host = strings.TrimSpace(s.Host)
	s.User = strings.TrimSpace(s.User)
	if err := validate(s); err != nil {
		fail(w, 400, err.Error())
		return
	}
	result, err := a.db.Exec("INSERT INTO servers(name,host,port,user,identity) VALUES(?,?,?,?,?)", s.Name, s.Host, s.Port, s.User, s.Identity)
	if err != nil {
		fail(w, 500, "No se pudo guardar")
		return
	}
	s.ID, _ = result.LastInsertId()
	reply(w, 201, s)
}
func (a *App) remove(w http.ResponseWriter, r *http.Request) {
	s, err := a.server(r)
	if err != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	// Serialize removal with snapshot publication: a late SSH response must not
	// be attached to a new server that SQLite gives the same numeric ID.
	a.mu.Lock()
	tx, err := a.db.Begin()
	if err != nil {
		a.mu.Unlock()
		fail(w, 500, "No se pudo eliminar")
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM native_swarm_apps WHERE server_id=?", s.ID); err == nil {
		_, err = tx.Exec("DELETE FROM dokploy_connections WHERE server_id=?", s.ID)
	}
	if err == nil {
		_, err = tx.Exec("DELETE FROM application_names WHERE server_id=?", s.ID)
	}
	if err == nil {
		_, err = tx.Exec("DELETE FROM samples WHERE server_id=?", s.ID)
	}
	if err == nil {
		_, err = tx.Exec("DELETE FROM terminal_sessions WHERE server_id=?", s.ID)
	}
	if err == nil {
		_, err = tx.Exec("DELETE FROM servers WHERE id=?", s.ID)
	}
	if err != nil || tx.Commit() != nil {
		tx.Rollback()
		a.mu.Unlock()
		fail(w, 500, "No se pudo eliminar")
		return
	}
	delete(a.states, s.ID)
	delete(a.docker, s.ID)
	a.mu.Unlock()
	reply(w, 200, map[string]bool{"ok": true})
}
func (a *App) history(w http.ResponseWriter, r *http.Request) {
	s, err := a.server(r)
	if err != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	rows, err := a.db.Query("SELECT at,cpu,memory,disk,uptime,rx,tx FROM (SELECT * FROM samples WHERE server_id=? ORDER BY at DESC LIMIT 240) ORDER BY at", s.ID)
	if err != nil {
		fail(w, 500, "No se pudo leer el histórico")
		return
	}
	defer rows.Close()
	out := []Sample{}
	for rows.Next() {
		var v Sample
		if rows.Scan(&v.At, &v.CPU, &v.Memory, &v.Disk, &v.Uptime, &v.RX, &v.TX) != nil {
			fail(w, 500, "Histórico inválido")
			return
		}
		out = append(out, v)
	}
	if rows.Err() != nil {
		fail(w, 500, "No se pudo leer el histórico")
		return
	}
	reply(w, 200, out)
}
func (a *App) check(w http.ResponseWriter, r *http.Request) {
	s, err := a.server(r)
	if err != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	reply(w, 200, a.collect(r.Context(), s))
}
func (a *App) containers(w http.ResponseWriter, r *http.Request) {
	s, e := a.server(r)
	if e != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	out, e := a.run(r.Context(), s, containersCommand)
	if e != nil {
		fail(w, 502, e.Error())
		return
	}
	items, e := parseContainers(out)
	if e != nil {
		fail(w, 502, "Respuesta Docker inválida")
		return
	}
	warnings := []string{}
	routes, routeErr := a.run(r.Context(), s, routesCommand)
	if routeErr != nil {
		warnings = append(warnings, "No se pudo leer la configuración de Dokploy; se muestran los enlaces detectados en etiquetas Docker.")
	} else {
		warnings = append(warnings, applyFileRoutes(items, routes)...)
	}
	result := []Container{}
	for _, item := range items {
		sortLinks(item.Links)
		result = append(result, item.Container)
	}
	reply(w, 200, map[string]any{"containers": result, "warnings": warnings})
}

var containerPattern = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

func (a *App) logs(w http.ResponseWriter, r *http.Request) {
	s, e := a.server(r)
	if e != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	id := r.PathValue("container")
	if !containerPattern.MatchString(id) {
		fail(w, 400, "ID de contenedor inválido")
		return
	}
	out, e := a.run(r.Context(), s, "docker logs --tail 200 --timestamps "+id+" 2>&1")
	if e != nil {
		fail(w, 502, e.Error())
		return
	}
	reply(w, 200, map[string]string{"logs": out})
}
