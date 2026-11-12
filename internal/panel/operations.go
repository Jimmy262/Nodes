package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type operationPlan struct {
	ID                   string           `json:"id"`
	Kind                 string           `json:"kind"`
	Name                 string           `json:"name"`
	Source               string           `json:"source"`
	Destination          string           `json:"destination,omitempty"`
	Manager              string           `json:"manager,omitempty"`
	Summary              []string         `json:"summary"`
	Warnings             []string         `json:"warnings"`
	Review               *operationReview `json:"review,omitempty"`
	Blockers             []string         `json:"blockers"`
	Expires              time.Time        `json:"expires"`
	URL                  string           `json:"url,omitempty"`
	DNS                  *domainStatus    `json:"dns,omitempty"`
	EnvironmentKeys      []string         `json:"environmentKeys,omitempty"`
	Workflow             string           `json:"workflow,omitempty"`
	TemplateID           string           `json:"templateId,omitempty"`
	ComposePreview       string           `json:"composePreview,omitempty"`
	deployWaitSeconds    int
	owner                string
	server, target       Server
	container            *containerSpec
	fingerprint          string
	archive              string
	compose              string
	project              string
	access               *deployAccess
	runtime, environment []byte
	hasDotEnv            bool
	edit                 *applicationEdit
	swarm                *swarmMigration
	github               *githubDeploymentPlan
	terminalInstall      *terminalInstallPlan
}

// A concise review can accompany the full technical summary without removing
// warnings or changing the confirmations required to execute an operation.
type operationReview struct {
	Summary  []string `json:"summary"`
	Warnings []string `json:"warnings"`
}
type operationManager struct {
	mu     sync.Mutex
	plans  map[string]*operationPlan
	busy   bool
	closed bool
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newOperationManager() *operationManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &operationManager{plans: map[string]*operationPlan{}, ctx: ctx, cancel: cancel}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				m.mu.Lock()
				m.expire()
				m.mu.Unlock()
			}
		}
	}()
	return m
}
func (m *operationManager) expire() {
	for id, p := range m.plans {
		if time.Now().After(p.Expires) {
			p.clean()
			delete(m.plans, id)
		}
	}
}
func (p *operationPlan) clean() {
	if p.archive != "" {
		os.Remove(p.archive)
	}
	p.container = nil
	clear(p.runtime)
	clear(p.environment)
	if p.github != nil {
		clear(p.github.policy)
		p.github = nil
	}
	if p.edit != nil {
		clear(p.edit.composeBytes)
		for _, values := range p.edit.variableFiles {
			clear(values)
		}
	}
	p.edit = nil
	p.swarm = nil
}
func (m *operationManager) close() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.plans {
		p.clean()
	}
	clear(m.plans)
}
func owner(r *http.Request) string {
	c, e := r.Cookie(sessionCookie)
	if e != nil {
		return ""
	}
	return tokenHash(c.Value)
}
func (a *App) savePlan(w http.ResponseWriter, r *http.Request, p *operationPlan) {
	p.Expires = time.Now().Add(10 * time.Minute)
	p.owner = owner(r)
	if p.ID == "" {
		var err error
		p.ID, err = randomToken()
		if err != nil {
			p.clean()
			fail(w, 500, "No se pudo preparar la operación")
			return
		}
	}
	m := a.operations
	m.mu.Lock()
	m.expire()
	if m.closed {
		m.mu.Unlock()
		p.clean()
		fail(w, 503, "Nodo se está cerrando; vuelve a abrirlo antes de preparar otra operación")
		return
	}
	if len(m.plans) >= 4 {
		m.mu.Unlock()
		p.clean()
		fail(w, 429, "Cierra o descarta las revisiones anteriores antes de preparar otra")
		return
	}
	m.plans[p.ID] = p
	m.mu.Unlock()
	reply(w, 200, p)
}
func (a *App) discardPlan(w http.ResponseWriter, r *http.Request) {
	m := a.operations
	m.mu.Lock()
	if p := m.plans[r.PathValue("operation")]; p != nil && p.owner == owner(r) {
		p.clean()
		delete(m.plans, p.ID)
	}
	m.mu.Unlock()
	reply(w, 200, map[string]bool{"ok": true})
}
func (a *App) rename(w http.ResponseWriter, r *http.Request) {
	s, e := a.server(r)
	if e != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if decode(w, r, &in) != nil {
		fail(w, 400, "Datos inválidos")
		return
	}
	s.Name = strings.TrimSpace(in.Name)
	if e = validate(s); e != nil || strings.ContainsAny(s.Name, "\r\n\x00") {
		fail(w, 400, "Indica un nombre de hasta 80 caracteres")
		return
	}
	if _, e = a.db.Exec("UPDATE servers SET name=? WHERE id=?", s.Name, s.ID); e != nil {
		fail(w, 500, "No se pudo guardar el nombre")
		return
	}
	reply(w, 200, s)
}
func (a *App) serverByID(id int64) (Server, error) {
	var s Server
	err := a.db.QueryRow("SELECT id,name,host,port,user,identity FROM servers WHERE id=?", id).Scan(&s.ID, &s.Name, &s.Host, &s.Port, &s.User, &s.Identity)
	return s, err
}

func (a *App) sameConnection(expected Server) bool {
	current, err := a.serverByID(expected.ID)
	return err == nil && current.Host == expected.Host && current.Port == expected.Port && current.User == expected.User && current.Identity == expected.Identity
}
func (a *App) containerPlan(w http.ResponseWriter, r *http.Request) {
	s, e := a.server(r)
	if e != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	id := r.PathValue("container")
	if len(id) != 64 || !containerPattern.MatchString(id) {
		fail(w, 400, "ID completo de contenedor inválido")
		return
	}
	var in struct {
		Kind     string `json:"kind"`
		TargetID int64  `json:"targetId"`
	}
	if decode(w, r, &in) != nil || (in.Kind != "delete" && in.Kind != "migrate") {
		fail(w, 400, "Operación inválida")
		return
	}
	c, e := a.inspectContainer(r.Context(), s, id)
	if e != nil {
		fail(w, 502, "No se pudo inspeccionar el contenedor actual")
		return
	}
	p := &operationPlan{Kind: in.Kind, Name: strings.TrimPrefix(c.Name, "/"), Source: s.Name, server: s, container: c, fingerprint: c.fingerprint(), Summary: []string{}, Warnings: []string{}, Blockers: []string{}, Manager: c.manager()}
	if c.protected() {
		p.Blockers = append(p.Blockers, "Este contenedor forma parte de la infraestructura de Dokploy o del proxy. Adminístralo desde su herramienta de origen.")
	}
	if in.Kind == "delete" {
		p.Summary = []string{"Se detendrá y eliminará únicamente este contenedor.", "Se conservarán la imagen, los volúmenes y las carpetas montadas."}
		p.Warnings = append(p.Warnings, "Los archivos guardados en la capa interna del contenedor se perderán. Los servicios que dependan de él pueden dejar de funcionar.")
		if c.manager() == "Swarm" {
			p.Blockers = append(p.Blockers, "Swarm volvería a crear esta tarea. Elimina el servicio o la aplicación desde Dokploy/Swarm.")
		}
		if c.manager() == "Compose" {
			p.Warnings = append(p.Warnings, "Compose puede volver a crearlo en el próximo despliegue. El archivo Compose original no se modifica.")
		}
	} else {
		t, e := a.serverByID(in.TargetID)
		if e != nil || t.ID == s.ID {
			fail(w, 400, "Escoge otro servidor de destino")
			return
		}
		p.target = t
		p.Destination = t.Name
		a.planMigration(r.Context(), p)
	}
	a.savePlan(w, r, p)
}
func (a *App) executeOperation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name         string `json:"name"`
		Acknowledged bool   `json:"acknowledged"`
	}
	if decode(w, r, &in) != nil {
		fail(w, 400, "Confirmación inválida")
		return
	}
	m := a.operations
	m.mu.Lock()
	m.expire()
	if m.closed {
		m.mu.Unlock()
		fail(w, 503, "Nodo se está cerrando; la operación no se inició")
		return
	}
	p := m.plans[r.PathValue("operation")]
	if p == nil || p.owner != owner(r) {
		m.mu.Unlock()
		fail(w, 409, "La revisión caducó. Vuelve a revisar la operación")
		return
	}
	if len(p.Blockers) > 0 || in.Name != p.Name || !in.Acknowledged {
		m.mu.Unlock()
		fail(w, 400, "Confirma los efectos y escribe el nombre exacto mostrado")
		return
	}
	if m.busy {
		m.mu.Unlock()
		fail(w, 409, "Hay otra operación en curso. Espera a que termine")
		return
	}
	if e := a.recordOperation(p); e != nil {
		m.mu.Unlock()
		fail(w, 500, "No se pudo registrar la operación")
		return
	}
	m.busy = true
	delete(m.plans, p.ID)
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer p.clean()
		defer func() { m.mu.Lock(); m.busy = false; m.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Minute)
		defer cancel()
		err := a.runOperation(ctx, p)
		if err != nil {
			a.operationState(p, "failed", err.Error())
		} else {
			a.operationState(p, "succeeded", successMessage(p))
		}
		// Refresh is automatic; a slow inventory read must not hold the operation lock.
		a.mu.Lock()
		snap := a.docker[p.server.ID]
		snap.attempt = time.Time{}
		a.docker[p.server.ID] = snap
		if p.target.ID != 0 {
			snap = a.docker[p.target.ID]
			snap.attempt = time.Time{}
			a.docker[p.target.ID] = snap
		}
		a.mu.Unlock()
	}()
	reply(w, 202, map[string]string{"id": p.ID})
}
func successMessage(p *operationPlan) string {
	switch p.Kind {
	case "terminal-install":
		return "Requisito instalado y comprobado. Puedes volver a abrir Codex o Terminal."
	case "github-setup":
		return "Conexión preparada y workflow publicado. Configura los secretos SSH en GitHub y pulsa Desplegar en la sección GitHub."
	case "github-deploy":
		return "Commit desplegado y comprobado por Docker Compose. Consulta GitHub para los despliegues automáticos."
	case "configure":
		return "Configuración guardada. " + p.edit.result
	case "delete":
		return "Contenedor eliminado. Sus volúmenes e imagen se conservaron."
	case "migrate":
		if p.swarm != nil {
			return "Servicio Swarm copiado y comprobado en el destino. El original queda detenido y conservado. Enlace: " + p.URL + ". HTTPS puede tardar unos minutos. La definición queda guardada en Nodo sin usar la API de Dokploy."
		}
		return "Copia iniciada en el destino. El original sigue detenido y conservado. Comprueba la aplicación y actualiza el proxy/DNS antes de eliminarlo."
	default:
		if p.URL != "" {
			return "Aplicación iniciada. Acceso configurado en " + p.URL + ". La emisión de HTTPS puede tardar unos minutos; comprueba el enlace."
		}
		return "Despliegue iniciado y comprobado por Docker Compose. Ya aparecerá en Aplicaciones."
	}
}
func (a *App) operationState(p *operationPlan, status, message string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := a.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE operations SET status=?,message=?,updated_at=? WHERE id=?", status, message, now, p.ID); err != nil {
		return
	}
	if _, err = tx.Exec("INSERT INTO operation_events(operation_id,status,message,at) VALUES(?,?,?,?)", p.ID, status, message, now); err != nil {
		return
	}
	if status != "running" {
		if _, err = tx.Exec("UPDATE operation_context SET finished_at=? WHERE operation_id=?", now, p.ID); err != nil {
			return
		}
	}
	tx.Commit()
}

// Call after reserving the port, so a second instance cannot interrupt records
// belonging to an already running panel.
func (a *App) RecoverOperations() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	message := "Nodo se reinició durante la operación. Revisa el recurso en el servidor antes de reintentar."
	if _, err = tx.Exec("INSERT INTO operation_events(operation_id,status,message,at) SELECT id,'interrupted',?,? FROM operations WHERE status='running'", message, now); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE operation_context SET finished_at=? WHERE operation_id IN (SELECT id FROM operations WHERE status='running')", now); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE operations SET status='interrupted',message=?,updated_at=? WHERE status='running'", message, now); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) listOperations(w http.ResponseWriter, r *http.Request) {
	rows, e := a.db.Query("SELECT o.id,o.kind,o.server_id,o.status,o.message,o.updated_at,COALESCE(c.details,''),COALESCE(c.started_at,''),COALESCE(c.finished_at,'') FROM operations o LEFT JOIN operation_context c ON c.operation_id=o.id ORDER BY o.updated_at DESC,o.rowid DESC LIMIT 20")
	if e != nil {
		fail(w, 500, "No se pudieron leer las operaciones")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, kind, status, message, at, details, started, finished string
		var sid int64
		if rows.Scan(&id, &kind, &sid, &status, &message, &at, &details, &started, &finished) != nil {
			fail(w, 500, "Operación inválida")
			return
		}
		var context operationDetails
		json.Unmarshal([]byte(details), &context)
		out = append(out, map[string]any{"id": id, "kind": kind, "serverId": sid, "status": status, "message": message, "at": at, "details": context, "startedAt": started, "finishedAt": finished, "events": []operationEvent{}})
	}
	if rows.Err() != nil {
		fail(w, 500, "No se pudo completar la lectura de operaciones")
		return
	}
	rows.Close()
	if e = a.operationEvents(out); e != nil {
		fail(w, 500, "No se pudieron leer los pasos de las operaciones")
		return
	}
	replySnapshot(w, r, out)
}
func (a *App) runOperation(ctx context.Context, p *operationPlan) error {
	if !a.sameConnection(p.server) {
		return errors.New("La conexión del servidor cambió o se retiró. Prepara una nueva revisión")
	}
	if p.Kind == "terminal-install" {
		return a.runTerminalInstall(ctx, p)
	}
	if p.Kind == "github-setup" {
		return a.runGithubSetup(ctx, p)
	}
	if p.Kind == "github-deploy" {
		return a.runGithubDeploy(ctx, p)
	}
	if p.Kind == "deploy" || p.Kind == "template-deploy" {
		return a.runDeploy(ctx, p)
	}
	if p.Kind == "configure" {
		return a.runApplicationEdit(ctx, p)
	}
	if p.Kind == "migrate" && p.swarm != nil {
		return a.runSwarmMigration(ctx, p)
	}
	c, e := a.inspectContainer(ctx, p.server, p.container.ID)
	if e != nil || c.fingerprint() != p.fingerprint {
		return errors.New("El contenedor cambió después de la revisión. Prepara una nueva operación")
	}
	p.container = c
	if p.Kind == "migrate" {
		return a.runMigration(ctx, p)
	}
	a.operationState(p, "running", "Deteniendo el contenedor "+p.Name+" (hasta 30 s para su cierre normal)…")
	if e = a.stream(ctx, p.server, "docker stop --time 30 "+c.ID+" >/dev/null", nil, io.Discard); e != nil {
		return errors.New("No se pudo confirmar la detención. No se envió la orden de eliminación; revisa el estado del contenedor")
	}
	a.operationState(p, "running", "Contenedor detenido. Eliminando "+p.Name+"; se conservarán sus volúmenes e imagen…")
	if e = a.stream(ctx, p.server, "docker rm "+c.ID+" >/dev/null", nil, io.Discard); e != nil {
		return errors.New("La detención terminó, pero Docker no confirmó la eliminación. El contenedor puede seguir detenido; revisa su estado")
	}
	return nil
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func jsonMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}
func errorStep(step string) error { return fmt.Errorf("No se pudo completar: %s", step) }
