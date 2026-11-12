package panel

import (
	"archive/zip"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"go.yaml.in/yaml/v3"
)

//go:embed templates/supabase
var composeAssets embed.FS

type templateVariable struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Help     string `json:"help"`
	Type     string `json:"type"`
	Default  string `json:"default"`
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
	Generate int    `json:"generate,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
}
type composeTemplate struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Category    string             `json:"category"`
	Version     string             `json:"version"`
	Source      string             `json:"source"`
	Service     string             `json:"service"`
	Port        int                `json:"port"`
	HostPort    int                `json:"hostPort"`
	Web         bool               `json:"web"`
	MemoryMiB   int                `json:"memoryMiB"`
	Notes       []string           `json:"notes"`
	Variables   []templateVariable `json:"variables"`
}

func templateCatalog() []composeTemplate {
	text := func(name, label, value, help string, required bool) templateVariable {
		return templateVariable{Name: name, Label: label, Default: value, Help: help, Type: "text", Required: required}
	}
	password := func(name, label, help string, n int) templateVariable {
		return templateVariable{Name: name, Label: label, Help: help, Type: "password", Required: true, Secret: true, Generate: n}
	}
	publicURL := text("PUBLIC_URL", "URL de acceso", "", "Con dominio se calcula automáticamente. Con IP/puerto indica la URL que abrirás en el navegador.", false)
	publicURL.Type = "url"
	database := text("POSTGRES_DB", "Base de datos", "app", "Se crea al iniciar por primera vez.", true)
	database.Pattern = `^[a-z][a-z0-9_]{0,62}$`
	user := text("POSTGRES_USER", "Usuario", "app", "Usuario administrador de esta instalación.", true)
	user.Pattern = `^[a-z][a-z0-9_]{0,62}$`
	timezone := text("GENERIC_TIMEZONE", "Zona horaria", "America/Lima", "Zona IANA que usarán los horarios de los flujos.", true)
	smtpPassword := password("SMTP_PASS", "Contraseña SMTP", "Opcional si tu servidor de correo no exige autenticación.", 0)
	smtpPassword.Required = false
	signup := text("ENABLE_EMAIL_SIGNUP", "Registro por correo", "false", "Actívalo después de configurar tu servidor SMTP; la confirmación de correo permanece habilitada.", true)
	signup.Type = "boolean"
	smtpPort := text("SMTP_PORT", "Puerto SMTP", "587", "Puerto del servidor de correo.", true)
	smtpPort.Type = "port"
	site := text("SITE_URL", "URL de tu aplicación cliente", "http://localhost:3000", "Destino permitido después de iniciar sesión; cambia localhost por tu frontend real.", true)
	site.Type = "url"
	supabasePassword := password("POSTGRES_PASSWORD", "Contraseña de PostgreSQL", "De 32 a 128 letras y números; se utiliza también en URLs internas de conexión.", 40)
	supabasePassword.Pattern = `^[A-Za-z0-9]{32,128}$`
	dashboardPassword := password("DASHBOARD_PASSWORD", "Contraseña de Studio", "De 32 a 128 letras y números para proteger el panel de administración.", 40)
	dashboardPassword.Pattern = `^[A-Za-z0-9]{32,128}$`
	return []composeTemplate{
		{ID: "postgresql", Name: "PostgreSQL", Description: "Base de datos relacional con almacenamiento persistente y comprobación de salud.", Category: "Datos", Version: "18.6-alpine", Source: "https://hub.docker.com/_/postgres", Service: "db", Port: 5432, HostPort: 5432, MemoryMiB: 512, Notes: []string{"El volumen se monta en /var/lib/postgresql, como requiere PostgreSQL 18.", "La contraseña y el usuario inicial solo se crean con un volumen vacío. Conserva un respaldo de tus credenciales y datos."}, Variables: []templateVariable{database, user, password("POSTGRES_PASSWORD", "Contraseña", "Mínimo 16 caracteres. Se genera una distinta por instalación.", 40)}},
		{ID: "redis", Name: "Redis", Description: "Caché y almacén de claves con contraseña, persistencia AOF y volumen propio.", Category: "Datos", Version: "8.10.3-alpine", Source: "https://hub.docker.com/_/redis", Service: "redis", Port: 6379, HostPort: 6379, MemoryMiB: 128, Notes: []string{"AOF conserva las escrituras en un volumen Docker.", "Por defecto solo se publica en 127.0.0.1 de la VPS. Usa un túnel SSH para acceder desde tu equipo."}, Variables: []templateVariable{password("REDIS_PASSWORD", "Contraseña de Redis", "Mínimo 16 caracteres. Se guarda en un archivo privado de configuración.", 40)}},
		{ID: "n8n", Name: "n8n", Description: "Automatización de flujos con PostgreSQL, clave de cifrado propia y datos persistentes.", Category: "Automatización", Version: "2.47.4", Source: "https://docs.n8n.io/hosting/installation/server-setups/docker-compose/", Service: "n8n", Port: 5678, HostPort: 5678, Web: true, MemoryMiB: 2048, Notes: []string{"Crea la cuenta propietaria al abrir n8n por primera vez.", "Guarda la clave de cifrado junto con tus respaldos: es necesaria para recuperar las credenciales de los flujos.", "La URL determina los webhooks y callbacks. Para uso público configura HTTPS."}, Variables: []templateVariable{publicURL, timezone, password("POSTGRES_PASSWORD", "Contraseña de PostgreSQL", "Credencial de la base de datos interna de n8n.", 40), password("N8N_ENCRYPTION_KEY", "Clave de cifrado", "Conserva esta clave para poder restaurar las credenciales de n8n.", 64)}},
		{ID: "supabase", Name: "Supabase", Description: "Stack de autoalojamiento: PostgreSQL, Auth, REST, Realtime, Storage, Studio, funciones y pooler.", Category: "Backend", Version: "self-hosted/v0.7.0", Source: "https://supabase.com/docs/guides/self-hosting/docker", Service: "kong", Port: 8000, HostPort: 8000, Web: true, MemoryMiB: 4096, Notes: []string{"Incluye el stack oficial v0.7.0 adaptado a proyectos aislados de Nodo; Analytics y Vector no están incluidos.", "Se generan claves ES256, claves publishable/secret y compatibilidad anon/service_role. Las claves de servicio son privadas.", "Solo el gateway se publica. Studio se protege con usuario y contraseña; PostgreSQL y el pooler quedan en la red interna.", "Los archivos y las claves quedan en la carpeta privada del despliegue. Respalda esa carpeta junto con los volúmenes Docker."}, Variables: []templateVariable{publicURL, site, text("ADDITIONAL_REDIRECT_URLS", "Otras URLs de redirección", "", "Opcional: separa las URLs permitidas con comas.", false), supabasePassword, text("DASHBOARD_USERNAME", "Usuario de Studio", "admin", "De 3 a 40 letras, números, guiones o guiones bajos.", true), dashboardPassword, signup, text("SMTP_HOST", "Servidor SMTP", "", "Necesario si activas el registro por correo.", false), smtpPort, text("SMTP_USER", "Usuario SMTP", "", "Opcional según tu servidor de correo.", false), smtpPassword, text("SMTP_ADMIN_EMAIL", "Correo remitente", "", "Necesario si activas el registro por correo.", false), text("SMTP_SENDER_NAME", "Nombre remitente", "Mi aplicación", "Nombre que se verá en los correos de autenticación.", false)}},
	}
}
func findTemplate(id string) (composeTemplate, bool) {
	for _, item := range templateCatalog() {
		if item.ID == id {
			return item, true
		}
	}
	return composeTemplate{}, false
}
func generatedHex(length int) (string, error) {
	b := make([]byte, (length+1)/2)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b)[:length], nil
}
func templateDefaults(item composeTemplate) (map[string]string, error) {
	values := map[string]string{}
	for _, v := range item.Variables {
		values[v.Name] = v.Default
		if v.Generate > 0 {
			generated, e := generatedHex(v.Generate)
			if e != nil {
				return nil, e
			}
			values[v.Name] = generated
		}
	}
	return values, nil
}
func (a *App) composeTemplates(w http.ResponseWriter, r *http.Request) {
	reply(w, 200, templateCatalog())
}
func (a *App) composeTemplateDefaults(w http.ResponseWriter, r *http.Request) {
	item, ok := findTemplate(r.PathValue("template"))
	if !ok {
		fail(w, 404, "Plantilla no encontrada")
		return
	}
	values, e := templateDefaults(item)
	if e != nil {
		fail(w, 500, "No se pudieron generar las credenciales")
		return
	}
	reply(w, 200, values)
}
func templateOrigin(raw string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("Indica una URL HTTP o HTTPS sin usuario, rutas ni parámetros")
	}
	if strings.ContainsAny(u.Host, "\r\n\t ") {
		return "", errors.New("URL inválida")
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return "", errors.New("Puerto de la URL inválido")
		}
	}
	return u.Scheme + "://" + u.Host, nil
}
func validateTemplateValues(item composeTemplate, input map[string]string) (map[string]string, error) {
	values := map[string]string{}
	known := map[string]bool{}
	for _, v := range item.Variables {
		known[v.Name] = true
		value, ok := input[v.Name]
		if !ok {
			value = v.Default
		}
		if v.Required && value == "" {
			return nil, fmt.Errorf("Completa %s", v.Label)
		}
		if len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("Valor inválido para %s", v.Label)
		}
		if value != "" {
			if v.Secret && v.Name != "SMTP_PASS" && len(value) < 16 {
				return nil, fmt.Errorf("%s debe tener al menos 16 caracteres", v.Label)
			}
			if v.Pattern != "" && !regexp.MustCompile(v.Pattern).MatchString(value) {
				return nil, fmt.Errorf("Revisa el formato de %s", v.Label)
			}
			switch v.Type {
			case "boolean":
				if value != "true" && value != "false" {
					return nil, fmt.Errorf("Elige Sí o No para %s", v.Label)
				}
			case "port":
				n, e := strconv.Atoi(value)
				if e != nil || n < 1 || n > 65535 {
					return nil, fmt.Errorf("%s debe estar entre 1 y 65535", v.Label)
				}
			case "url":
				if _, e := templateOrigin(value); e != nil {
					return nil, fmt.Errorf("%s: %s", v.Label, e)
				}
			}
		}
		values[v.Name] = value
	}
	for key := range input {
		if !known[key] {
			return nil, fmt.Errorf("Variable no admitida: %s", key)
		}
	}
	if item.ID == "n8n" {
		if _, e := time.LoadLocation(values["GENERIC_TIMEZONE"]); e != nil {
			return nil, errors.New("Indica una zona horaria IANA válida, por ejemplo America/Lima")
		}
	}
	if item.ID == "supabase" {
		if !regexp.MustCompile(`^[A-Za-z0-9_-]{3,40}$`).MatchString(values["DASHBOARD_USERNAME"]) {
			return nil, errors.New("El usuario de Studio necesita de 3 a 40 letras, números, guiones o guiones bajos")
		}
		if values["ENABLE_EMAIL_SIGNUP"] == "true" {
			if values["SMTP_HOST"] == "" || values["SMTP_ADMIN_EMAIL"] == "" {
				return nil, errors.New("Configura servidor y correo remitente SMTP para activar el registro por correo")
			}
			if _, e := mail.ParseAddress(values["SMTP_ADMIN_EMAIL"]); e != nil {
				return nil, errors.New("Revisa el correo remitente SMTP")
			}
		}
		if (values["SMTP_USER"] == "") != (values["SMTP_PASS"] == "") {
			return nil, errors.New("Completa tanto el usuario como la contraseña SMTP, o deja ambos vacíos")
		}
	}
	return values, nil
}

type templateDeployInput struct {
	Name     string            `json:"name"`
	Values   map[string]string `json:"values"`
	Access   string            `json:"access"`
	Domain   string            `json:"domain"`
	HTTPS    bool              `json:"https"`
	HostPort int               `json:"hostPort"`
	Bind     string            `json:"bind"`
}

func (a *App) composeTemplatePlan(w http.ResponseWriter, r *http.Request) {
	s, e := a.server(r)
	if e != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	item, ok := findTemplate(r.PathValue("template"))
	if !ok {
		fail(w, 404, "Plantilla no encontrada")
		return
	}
	var in templateDeployInput
	r.Body = http.MaxBytesReader(w, r.Body, 80<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
		fail(w, 400, "Configuración de plantilla inválida")
		return
	}
	if !projectPattern.MatchString(in.Name) {
		fail(w, 400, "Usa un nombre de 2–40 letras minúsculas, números o guiones, empezando por una letra")
		return
	}
	if in.Bind != "127.0.0.1" && in.Bind != "0.0.0.0" {
		fail(w, 400, "Elige acceso local o público")
		return
	}
	if in.HostPort < 1 || in.HostPort > 65535 {
		fail(w, 400, "El puerto debe estar entre 1 y 65535")
		return
	}
	if in.Access != "port" && in.Access != "internal" && (!item.Web || (in.Access != "auto" && in.Access != "custom")) {
		fail(w, 400, "Tipo de acceso no admitido para esta plantilla")
		return
	}
	if item.Web && in.Access == "internal" {
		fail(w, 400, "Configura un enlace para acceder a esta aplicación")
		return
	}
	if item.Web && in.Access != "port" {
		if in.Values == nil {
			in.Values = map[string]string{}
		}
		in.Values["PUBLIC_URL"] = ""
	}
	values, e := validateTemplateValues(item, in.Values)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	id, e := randomToken()
	if e != nil {
		fail(w, 500, "No se pudo preparar el despliegue")
		return
	}
	p := &operationPlan{ID: id, Kind: "template-deploy", Name: in.Name, Source: s.Name, Manager: "Compose · " + item.Name, server: s, project: in.Name + "-" + id[:8], compose: "compose.yaml", Summary: []string{"Plantilla: " + item.Name + " · " + item.Version, "Proyecto nuevo: " + in.Name + "-" + id[:8] + ". Se crearán volúmenes propios."}, Warnings: append([]string{}, item.Notes...), Blockers: []string{}, TemplateID: item.ID}
	if in.Access == "auto" || in.Access == "custom" {
		if e = a.prepareAccess(r.Context(), p, in.Access, in.Domain, item.Service, item.Port, in.HTTPS); e != nil {
			fail(w, 400, e.Error())
			return
		}
	}
	if item.Web {
		if p.access == nil {
			origin, e := templateOrigin(values["PUBLIC_URL"])
			if e != nil {
				fail(w, 400, "URL de acceso: "+e.Error())
				return
			}
			p.URL = origin
			p.Summary = append(p.Summary, "La URL indicada se usará para los enlaces, callbacks y webhooks; el DNS y cualquier proxy externo deben apuntar a esta VPS.")
		}
		values["PUBLIC_URL"] = p.URL
		if strings.HasPrefix(p.URL, "http://") {
			p.Warnings = append(p.Warnings, "El acceso elegido usa HTTP. Configura HTTPS antes de usar credenciales por Internet.")
		}
	}
	if in.Access == "internal" {
		p.Summary = append(p.Summary, "Sin puertos publicados en la VPS. Los servicios se comunican dentro de la red de este proyecto.")
	} else if p.access == nil {
		p.Summary = append(p.Summary, fmt.Sprintf("Puerto de la VPS: %s:%d → %s:%d", in.Bind, in.HostPort, item.Service, item.Port))
		if in.Bind == "0.0.0.0" {
			p.Warnings = append(p.Warnings, "El puerto estará publicado en todas las interfaces. Restringe el acceso con el firewall de tu VPS.")
		} else {
			p.Warnings = append(p.Warnings, "El puerto solo acepta conexiones en la propia VPS. Para abrirlo desde tu equipo usa un túnel SSH.")
		}
	}
	config, files, env, e := renderComposeTemplate(item, values, p.project, in.HostPort, in.Bind, in.Access, p.access != nil)
	if e != nil {
		fail(w, 500, "No se pudo preparar la plantilla: "+e.Error())
		return
	}
	data, e := composeTemplateZip(config, files)
	if e != nil {
		fail(w, 500, "No se pudo empaquetar la plantilla")
		return
	}
	z, e := inspectZip(data, in.HostPort, item.Port)
	if e != nil {
		fail(w, 500, "La plantilla no pasó la validación: "+e.Error())
		return
	}
	// Values are read from a private dotenv file at Compose invocation, not
	// injected into unrelated containers or returned in the review JSON.
	if e = configureDeployment(z, p, nil, ""); e != nil {
		fail(w, 400, e.Error())
		return
	}
	preview, e := yaml.Marshal(z.config)
	if e != nil {
		fail(w, 500, "No se pudo preparar la vista previa del Compose")
		return
	}
	archive, e := writeProjectTar(z)
	if e != nil {
		fail(w, 500, "No se pudo preparar el archivo de despliegue")
		return
	}
	p.archive = archive
	p.runtime = z.runtime
	p.environment = environmentFile(env)
	for _, value := range env {
		p.EnvironmentKeys = append(p.EnvironmentKeys, value.Name)
	}
	sort.Strings(p.EnvironmentKeys)
	p.ComposePreview = string(preview)
	p.Summary = append(p.Summary, fmt.Sprintf("%d servicios · %d variables · %d archivos auxiliares", len(z.services), len(env), len(files)))
	if item.ID == "supabase" {
		p.deployWaitSeconds = 600
		p.Warnings = append(p.Warnings, "El primer inicio de Supabase puede tardar varios minutos mientras se inicializan sus servicios.")
	}
	a.mu.RLock()
	memory := a.states[s.ID].Sample
	a.mu.RUnlock()
	if memory != nil && memory.MemoryTotal > 0 && memory.MemoryTotal < float64(item.MemoryMiB)*(1<<20) {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Esta VPS tiene menos de %d MiB de RAM. Revisa los recursos antes de iniciar %s.", item.MemoryMiB, item.Name))
	}
	if _, e = a.run(r.Context(), s, `docker info >/dev/null && docker compose version >/dev/null && command -v tar >/dev/null || exit 1; help=$(docker compose up --help) || exit 1; case "$help" in *--wait-timeout*) ;; *) exit 1;; esac`); e != nil {
		p.Blockers = append(p.Blockers, "El servidor necesita Docker, Docker Compose v2 y tar accesibles mediante SSH.")
	}
	a.savePlan(w, r, p)
}

func composeTemplateZip(config map[string]any, files map[string][]byte) ([]byte, error) {
	raw, e := json.Marshal(config)
	if e != nil {
		return nil, e
	}
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	entries := map[string][]byte{"compose.yaml": raw}
	for name, data := range files {
		if !safeArchivePath(name) || name == "compose.yaml" {
			return nil, errors.New("Archivo auxiliar inválido")
		}
		entries[name] = data
	}
	for _, name := range templateKeys(entries) {
		dst, e := writer.Create(name)
		if e != nil {
			return nil, e
		}
		if _, e = dst.Write(entries[name]); e != nil {
			return nil, e
		}
	}
	if e = writer.Close(); e != nil {
		return nil, e
	}
	return out.Bytes(), nil
}
func templateEnvironment(values map[string]string) []envValue {
	out := []envValue{}
	for _, key := range templateKeys(values) {
		out = append(out, envValue{Name: key, Value: values[key]})
	}
	return out
}
func templateKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func renderComposeTemplate(item composeTemplate, values map[string]string, project string, hostPort int, bind, access string, proxy bool) (map[string]any, map[string][]byte, []envValue, error) {
	config := map[string]any{}
	files := map[string][]byte{}
	health := func(command []string) map[string]any {
		return map[string]any{"test": command, "interval": "10s", "timeout": "5s", "retries": 10, "start_period": "20s"}
	}
	postgres := func(user, db string) map[string]any {
		return map[string]any{"image": "postgres:18.6-alpine", "restart": "unless-stopped", "shm_size": "128mb", "environment": map[string]any{"POSTGRES_USER": user, "POSTGRES_DB": db, "POSTGRES_PASSWORD": "${POSTGRES_PASSWORD}"}, "volumes": []any{"db-data:/var/lib/postgresql"}, "healthcheck": health([]string{"CMD-SHELL", `pg_isready -U "$${POSTGRES_USER}" -d "$${POSTGRES_DB}"`})}
	}
	switch item.ID {
	case "postgresql":
		config = map[string]any{"services": map[string]any{"db": postgres("${POSTGRES_USER}", "${POSTGRES_DB}")}, "volumes": map[string]any{"db-data": map[string]any{}}}
	case "redis":
		password, _ := json.Marshal(values["REDIS_PASSWORD"])
		files["config/redis.conf"] = []byte("bind 0.0.0.0\nprotected-mode yes\nappendonly yes\ndir /data\nrequirepass " + string(password) + "\n")
		config = map[string]any{"services": map[string]any{"redis": map[string]any{"image": "redis:8.10.3-alpine", "restart": "unless-stopped", "command": []any{"redis-server", "/usr/local/etc/redis/redis.conf"}, "environment": map[string]any{"REDISCLI_AUTH": "${REDIS_PASSWORD}"}, "volumes": []any{"redis-data:/data", "./config/redis.conf:/usr/local/etc/redis/redis.conf:ro"}, "healthcheck": health([]string{"CMD-SHELL", "redis-cli --raw ping | grep -qx PONG"})}}, "volumes": map[string]any{"redis-data": map[string]any{}}}
	case "n8n":
		u, _ := url.Parse(values["PUBLIC_URL"])
		values["N8N_HOST"] = u.Hostname()
		values["N8N_PROTOCOL"] = u.Scheme
		values["WEBHOOK_URL"] = values["PUBLIC_URL"] + "/"
		values["N8N_EDITOR_BASE_URL"] = values["PUBLIC_URL"]
		values["N8N_SECURE_COOKIE"] = strconv.FormatBool(u.Scheme == "https")
		hops := "0"
		if proxy {
			hops = "1"
		}
		config = map[string]any{"services": map[string]any{"db": postgres("n8n", "n8n"), "n8n": map[string]any{"image": "docker.n8n.io/n8nio/n8n:2.47.4", "restart": "unless-stopped", "depends_on": map[string]any{"db": map[string]any{"condition": "service_healthy"}}, "environment": map[string]any{"DB_TYPE": "postgresdb", "DB_POSTGRESDB_HOST": "db", "DB_POSTGRESDB_PORT": "5432", "DB_POSTGRESDB_DATABASE": "n8n", "DB_POSTGRESDB_USER": "n8n", "DB_POSTGRESDB_PASSWORD": "${POSTGRES_PASSWORD}", "N8N_ENCRYPTION_KEY": "${N8N_ENCRYPTION_KEY}", "GENERIC_TIMEZONE": "${GENERIC_TIMEZONE}", "TZ": "${GENERIC_TIMEZONE}", "N8N_HOST": "${N8N_HOST}", "N8N_PROTOCOL": "${N8N_PROTOCOL}", "N8N_PORT": "5678", "WEBHOOK_URL": "${WEBHOOK_URL}", "N8N_EDITOR_BASE_URL": "${N8N_EDITOR_BASE_URL}", "N8N_PROXY_HOPS": hops, "N8N_SECURE_COOKIE": "${N8N_SECURE_COOKIE}", "N8N_ENFORCE_SETTINGS_FILE_PERMISSIONS": "true", "N8N_DIAGNOSTICS_ENABLED": "false", "N8N_RUNNERS_ENABLED": "true"}, "volumes": []any{"n8n-data:/home/node/.n8n"}, "healthcheck": health([]string{"CMD", "node", "-e", `fetch('http://127.0.0.1:5678/healthz/readiness').then(r=>{if(!r.ok)process.exit(1)}).catch(()=>process.exit(1))`})}}, "volumes": map[string]any{"db-data": map[string]any{}, "n8n-data": map[string]any{}}}
	case "supabase":
		var e error
		config, files, e = renderSupabase(values, project)
		if e != nil {
			return nil, nil, nil, e
		}
	default:
		return nil, nil, nil, errors.New("Plantilla desconocida")
	}
	services := config["services"].(map[string]any)
	if !proxy && access != "internal" {
		services[item.Service].(map[string]any)["ports"] = []any{fmt.Sprintf("%s:%d:%d", bind, hostPort, item.Port)}
	}
	return config, files, templateEnvironment(values), nil
}

func renderSupabase(values map[string]string, project string) (map[string]any, map[string][]byte, error) {
	raw, e := composeAssets.ReadFile("templates/supabase/docker-compose.yml")
	if e != nil {
		return nil, nil, e
	}
	var config map[string]any
	if e = yaml.Unmarshal(raw, &config); e != nil {
		return nil, nil, e
	}
	delete(config, "name")
	defaults := map[string]string{"POSTGRES_HOST": "db", "POSTGRES_DB": "postgres", "POSTGRES_PORT": "5432", "POOLER_DEFAULT_POOL_SIZE": "20", "POOLER_MAX_CLIENT_CONN": "100", "POOLER_DB_POOL_SIZE": "5", "POOLER_TENANT_ID": project, "STUDIO_DEFAULT_ORGANIZATION": "Nodo", "STUDIO_DEFAULT_PROJECT": project, "OPENAI_API_KEY": "", "JWT_EXPIRY": "3600", "DISABLE_SIGNUP": "false", "ENABLE_EMAIL_AUTOCONFIRM": "false", "ENABLE_ANONYMOUS_USERS": "false", "ENABLE_PHONE_SIGNUP": "false", "ENABLE_PHONE_AUTOCONFIRM": "false", "GLOBAL_S3_BUCKET": "storage", "REGION": "local", "STORAGE_TENANT_ID": project, "FUNCTIONS_VERIFY_JWT": "true", "PGRST_DB_SCHEMAS": "public,graphql_public", "PGRST_DB_MAX_ROWS": "1000", "PGRST_DB_EXTRA_SEARCH_PATH": "public", "IMGPROXY_AUTO_WEBP": "true"}
	for _, key := range []string{"MAILER_URLPATHS_CONFIRMATION", "MAILER_URLPATHS_INVITE", "MAILER_URLPATHS_RECOVERY", "MAILER_URLPATHS_EMAIL_CHANGE"} {
		defaults[key] = "/auth/v1/verify"
	}
	defaults["SUPABASE_PUBLIC_URL"] = values["PUBLIC_URL"]
	defaults["API_EXTERNAL_URL"] = values["PUBLIC_URL"] + "/auth/v1"
	for key, value := range defaults {
		values[key] = value
	}
	for key, length := range map[string]int{"JWT_SECRET": 64, "SECRET_KEY_BASE": 96, "REALTIME_DB_ENC_KEY": 16, "VAULT_ENC_KEY": 32, "PG_META_CRYPTO_KEY": 64, "S3_PROTOCOL_ACCESS_KEY_ID": 32, "S3_PROTOCOL_ACCESS_KEY_SECRET": 64} {
		values[key], e = generatedHex(length)
		if e != nil {
			return nil, nil, e
		}
	}
	if e = supabaseAuthKeys(values); e != nil {
		return nil, nil, e
	}
	services := config["services"].(map[string]any)
	for _, entry := range services {
		s := entry.(map[string]any)
		delete(s, "container_name")
		delete(s, "ports")
	}
	realtime := services["realtime"].(map[string]any)
	realtime["hostname"] = "realtime-dev.supabase-realtime"
	realtime["networks"] = map[string]any{"default": map[string]any{"aliases": []any{"realtime-dev.supabase-realtime"}}}
	services["auth"].(map[string]any)["environment"].(map[string]any)["GOTRUE_JWT_KEYS"] = "${JWT_KEYS}"
	realtime["environment"].(map[string]any)["API_JWT_JWKS"] = "${JWT_JWKS}"
	services["storage"].(map[string]any)["environment"].(map[string]any)["JWT_JWKS"] = "${JWT_JWKS}"
	volumes := config["volumes"].(map[string]any)
	for _, name := range []string{"postgres-data", "storage-data", "snippets"} {
		volumes[name] = map[string]any{}
	}
	for _, entry := range services {
		s := entry.(map[string]any)
		mounts, _ := s["volumes"].([]any)
		for i, mount := range mounts {
			value := mount.(string)
			switch {
			case strings.HasPrefix(value, "./volumes/db/data:"):
				mounts[i] = "postgres-data:/var/lib/postgresql/data"
			case strings.HasPrefix(value, "./volumes/storage:"):
				mounts[i] = "storage-data:/var/lib/storage"
			case strings.HasPrefix(value, "./volumes/snippets:"):
				mounts[i] = "snippets:/app/snippets"
			}
		}
	}
	files := map[string][]byte{}
	e = fs.WalkDir(composeAssets, "templates/supabase", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.HasSuffix(name, "docker-compose.yml") {
			return nil
		}
		data, e := composeAssets.ReadFile(name)
		if e != nil {
			return e
		}
		files[strings.TrimPrefix(name, "templates/supabase/")] = data
		return nil
	})
	return config, files, e
}

// Generate the same key shapes as Supabase's self-hosted add-new-auth-keys.sh.
// JWT_JWKS includes the legacy symmetric verifier and stays server-side.
func supabaseAuthKeys(values map[string]string) error {
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return e
	}
	kid, e := generatedHex(32)
	if e != nil {
		return e
	}
	b64 := base64.RawURLEncoding.EncodeToString
	x := key.X.FillBytes(make([]byte, 32))
	y := key.Y.FillBytes(make([]byte, 32))
	d := key.D.FillBytes(make([]byte, 32))
	public := map[string]any{"kty": "EC", "kid": kid, "use": "sig", "key_ops": []string{"verify"}, "alg": "ES256", "ext": true, "crv": "P-256", "x": b64(x), "y": b64(y)}
	private := map[string]any{}
	for k, v := range public {
		private[k] = v
	}
	private["d"] = b64(d)
	private["key_ops"] = []string{"sign", "verify"}
	oct := map[string]any{"kty": "oct", "alg": "HS256", "k": b64([]byte(values["JWT_SECRET"]))}
	raw, _ := json.Marshal([]any{private, oct})
	values["JWT_KEYS"] = string(raw)
	raw, _ = json.Marshal(map[string]any{"keys": []any{public, oct}})
	values["JWT_JWKS"] = string(raw)
	issued := time.Now().Unix()
	expiry := issued + 5*365*24*3600
	for _, role := range []string{"anon", "service_role"} {
		payload, _ := json.Marshal(map[string]any{"role": role, "iss": "supabase", "iat": issued, "exp": expiry})
		legacyHeader, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
		input := b64(legacyHeader) + "." + b64(payload)
		mac := hmac.New(sha256.New, []byte(values["JWT_SECRET"]))
		mac.Write([]byte(input))
		legacy := input + "." + b64(mac.Sum(nil))
		header, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT", "kid": kid})
		input = b64(header) + "." + b64(payload)
		digest := sha256.Sum256([]byte(input))
		r, s, e := ecdsa.Sign(rand.Reader, key, digest[:])
		if e != nil {
			return e
		}
		signature := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
		name := "ANON_KEY"
		if role == "service_role" {
			name = "SERVICE_ROLE_KEY"
		}
		values[name] = legacy
		values[name+"_ASYMMETRIC"] = input + "." + b64(signature)
	}
	for _, kind := range []string{"publishable", "secret"} {
		random := make([]byte, 17)
		if _, e = rand.Read(random); e != nil {
			return e
		}
		intermediate := "sb_" + kind + "_" + b64(random)[:22]
		checksum := sha256.Sum256([]byte("supabase-self-hosted|" + intermediate))
		values["SUPABASE_"+strings.ToUpper(kind)+"_KEY"] = intermediate + "_" + b64(checksum[:])[:8]
	}
	return nil
}
