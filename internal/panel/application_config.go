package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

type configurationService struct {
	Name      string     `json:"name"`
	Variables []envValue `json:"variables"`
}
type editableDomain struct {
	ID      string `json:"id"`
	Host    string `json:"host"`
	Service string `json:"service"`
	Port    int    `json:"port"`
	HTTPS   bool   `json:"https"`
	Path    string `json:"path"`
	raw     map[string]any
}
type applicationConfiguration struct {
	ID                      string                 `json:"id"`
	Name                    string                 `json:"name"`
	Revision                string                 `json:"revision"`
	Manager                 string                 `json:"manager"`
	Message                 string                 `json:"message"`
	ConnectionRequired      bool                   `json:"connectionRequired"`
	CanEdit                 bool                   `json:"canEdit"`
	EnvironmentMode         string                 `json:"environmentMode"`
	Environment             []configurationService `json:"environment"`
	EnvText                 string                 `json:"envText"`
	Domains                 []editableDomain       `json:"domains"`
	PublicIP                string                 `json:"publicIp"`
	appName, kind, recordID string
	record                  map[string]any
	compose                 *editableCompose
}
type domainChange struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	Automatic bool   `json:"automatic"`
	Service   string `json:"service"`
	Port      int    `json:"port"`
	HTTPS     bool   `json:"https"`
}
type applicationEditInput struct {
	AppID       string                 `json:"appId"`
	Revision    string                 `json:"revision"`
	Name        string                 `json:"name"`
	Environment []configurationService `json:"environment"`
	EnvText     *string                `json:"envText"`
	Domain      *domainChange          `json:"domain"`
}
type applicationEdit struct {
	before                                 *applicationConfiguration
	input                                  applicationEditInput
	nameChanged, envChanged, domainChanged bool
	composeBytes                           []byte
	variableFiles                          map[string][]byte
	services                               []string
	result                                 string
}

func (a *App) applicationNames() (map[string]string, error) {
	rows, err := a.db.Query("SELECT app_id,name FROM application_names")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
func (a *App) applicationMetadata(ctx context.Context, s Server, id string) (*Application, []*inspectedContainer, error) {
	if len(id) > 300 || !strings.HasPrefix(id, fmt.Sprintf("%d:", s.ID)) {
		return nil, nil, errors.New("Aplicación inválida")
	}
	out, err := a.run(ctx, s, containersCommand)
	if err != nil {
		return nil, nil, errors.New("No se pudo actualizar la aplicación")
	}
	items, err := parseContainers(out)
	if err != nil {
		return nil, nil, err
	}
	for _, app := range groupApplications(s.ID, items, nil) {
		if app.ID != id {
			continue
		}
		selected := []*inspectedContainer{}
		for _, service := range app.Services {
			for _, item := range items {
				if item.ID == service.ID {
					selected = append(selected, item)
				}
			}
		}
		names, e := a.applicationNames()
		if e != nil {
			return nil, nil, e
		}
		if name := names[id]; name != "" {
			app.Name = name
		}
		return &app, selected, nil
	}
	return nil, nil, errors.New("La aplicación ya no está en este servidor. Actualiza el inventario")
}
func (a *App) loadApplicationConfiguration(ctx context.Context, s Server, id string) (*applicationConfiguration, error) {
	app, items, err := a.applicationMetadata(ctx, s, id)
	if err != nil {
		return nil, err
	}
	c := &applicationConfiguration{ID: id, Name: app.Name, Manager: "Docker", Environment: []configurationService{}, Domains: []editableDomain{}, PublicIP: sshPublicIP(ctx, s)}
	labels := items[0].Labels
	project := labels["com.docker.compose.project"]
	swarm := labels["com.docker.swarm.service.name"]
	protected := false
	for _, item := range items {
		n := strings.ToLower(item.Names)
		if strings.HasPrefix(n, "dokploy") || strings.HasPrefix(n, "traefik") {
			protected = true
		}
	}
	if protected {
		c.Message = "Infraestructura del servidor. Puedes cambiar su nombre visible en Nodo."
	} else if adopted, nativeErr := a.nativeSwarmConfigurationIfPresent(ctx, s, c, swarm); nativeErr != nil {
		return nil, nativeErr
	} else if adopted {
		// The definition belongs to Nodo; no API connection to Dokploy is needed.
	} else if swarm != "" || strings.Contains(labels["com.docker.compose.project.working_dir"], "/etc/dokploy/") {
		c.Manager = "Dokploy"
		c.appName = project
		c.kind = "compose"
		if swarm != "" {
			c.appName = swarm
			c.kind = "application"
		}
		key, e := a.dokployKey(s)
		if e != nil {
			c.ConnectionRequired = true
			c.Message = "Conecta Dokploy una vez para editar sus dominios y variables desde aquí."
		} else {
			defer clear(key)
			if err = a.loadDokployConfiguration(ctx, s, key, c); err != nil {
				c.ConnectionRequired = true
				c.CanEdit = false
				c.Message = err.Error()
				c.EnvText = ""
				c.record = nil
				c.Domains = []editableDomain{}
			} else {
				seen := map[string]bool{}
				for _, item := range items {
					service := item.Labels["com.docker.compose.service"]
					if service != "" && !seen[service] {
						seen[service] = true
						c.Environment = append(c.Environment, configurationService{Name: service, Variables: []envValue{}})
					}
				}
				sort.Slice(c.Environment, func(i, j int) bool { return c.Environment[i].Name < c.Environment[j].Name })
			}
		}
	} else if project != "" {
		c.Manager = "Docker Compose"
		c.appName = project
		c.kind = "compose-file"
		if err = a.loadComposeConfiguration(ctx, s, items, c); err != nil {
			c.Message = err.Error()
			c.compose = nil
			c.Environment = []configurationService{}
			c.Domains = []editableDomain{}
		} else {
			c.CanEdit = true
			c.EnvironmentMode = "services"
		}
	} else {
		c.Message = "Este contenedor no tiene un proyecto Compose asociado. Puedes cambiar su nombre visible; para editar su configuración hay que conservar su definición de despliegue."
	}
	// Include the complete source configuration in the revision, never expose it.
	domainSources := []any{}
	for _, d := range c.Domains {
		domainSources = append(domainSources, d.raw)
	}
	b, _ := json.Marshal([]any{c.ID, c.Name, c.Manager, c.ConnectionRequired, c.Environment, c.EnvText, c.Domains, domainSources, c.record, c.compose})
	c.Revision = hashBytes(b)
	return c, nil
}
func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (a *App) getApplicationConfiguration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s, e := a.server(r)
	if e != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	c, e := a.loadApplicationConfiguration(r.Context(), s, r.URL.Query().Get("appId"))
	if e != nil {
		fail(w, 502, e.Error())
		return
	}
	reply(w, 200, c)
}
func decodeConfiguration(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("JSON inválido")
	}
	return nil
}
func (a *App) applicationConfigurationPlan(w http.ResponseWriter, r *http.Request) {
	s, e := a.server(r)
	if e != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	var in applicationEditInput
	if decodeConfiguration(w, r, &in) != nil {
		fail(w, 400, "Configuración inválida o demasiado grande")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 80 || strings.ContainsAny(in.Name, "\x00\r\n") {
		fail(w, 400, "Indica un nombre de 1 a 80 caracteres")
		return
	}
	c, e := a.loadApplicationConfiguration(r.Context(), s, in.AppID)
	if e != nil {
		fail(w, 502, e.Error())
		return
	}
	if in.Revision != c.Revision {
		fail(w, 409, "La configuración cambió. Cierra y vuelve a abrir el editor para cargar la versión actual")
		return
	}
	edit := &applicationEdit{before: c, input: in, nameChanged: in.Name != c.Name}
	p := &operationPlan{Kind: "configure", Name: in.Name, Source: s.Name, Manager: c.Manager, server: s, Summary: []string{}, Warnings: []string{}, Blockers: []string{}, edit: edit}
	p.ID, e = randomToken()
	if e != nil {
		fail(w, 500, "No se pudo preparar el cambio")
		return
	}
	if edit.nameChanged {
		p.Summary = append(p.Summary, "Nombre: "+c.Name+" → "+in.Name)
	}
	if len(in.Environment) > 0 {
		if !c.CanEdit || c.EnvironmentMode != "services" {
			fail(w, 400, "Este proyecto no admite variables por servicio")
			return
		}
		seen := map[string]bool{}
		for _, service := range in.Environment {
			if seen[service.Name] {
				fail(w, 400, "Servicio duplicado")
				return
			}
			seen[service.Name] = true
			var current *configurationService
			for i := range c.Environment {
				if c.Environment[i].Name == service.Name {
					current = &c.Environment[i]
				}
			}
			if current == nil {
				fail(w, 400, "Servicio desconocido")
				return
			}
			b, _ := json.Marshal(service.Variables)
			if _, e = validateEnvironment(string(b)); e != nil {
				fail(w, 400, e.Error())
				return
			}
			old := map[string]string{}
			for _, v := range current.Variables {
				old[v.Name] = v.Value
			}
			for _, v := range service.Variables {
				value, ok := old[v.Name]
				if !ok || value != v.Value {
					edit.envChanged = true
					p.EnvironmentKeys = append(p.EnvironmentKeys, service.Name+": "+v.Name)
				}
			}
		}
	}
	if in.EnvText != nil {
		if !c.CanEdit || c.EnvironmentMode != "dotenv" {
			fail(w, 400, "Este proyecto no admite un archivo de variables")
			return
		}
		if len(*in.EnvText) > 128<<10 || strings.ContainsRune(*in.EnvText, 0) {
			fail(w, 400, "Las variables superan el límite permitido")
			return
		}
		edit.envChanged = *in.EnvText != c.EnvText
	}
	if edit.envChanged {
		p.Summary = append(p.Summary, "Se actualizarán las variables de entorno indicadas.")
	}
	if in.Domain != nil {
		if !c.CanEdit {
			fail(w, 400, "Conecta el administrador del proyecto para cambiar su dominio")
			return
		}
		d := in.Domain
		d.Host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d.Host), "."))
		if d.ID != "" {
			var existing *editableDomain
			for i := range c.Domains {
				if c.Domains[i].ID == d.ID {
					existing = &c.Domains[i]
				}
			}
			if existing == nil {
				fail(w, 400, "Dominio desconocido")
				return
			}
			d.Service = existing.Service
			d.Port = existing.Port
			d.HTTPS = existing.HTTPS
			edit.domainChanged = d.Automatic || !strings.EqualFold(d.Host, existing.Host)
		} else {
			edit.domainChanged = true
		}
		if edit.domainChanged {
			if d.ID == "" && c.kind != "application" {
				found := false
				for _, service := range c.Environment {
					if service.Name == d.Service {
						found = true
					}
				}
				if !found {
					fail(w, 400, "Escoge el servicio que recibirá el dominio")
					return
				}
			}
			mode := "custom"
			if d.Automatic {
				mode = "auto"
			}
			p.project = "app-" + p.ID[:12]
			if e = a.prepareAccess(r.Context(), p, mode, d.Host, d.Service, d.Port, d.HTTPS); e != nil {
				fail(w, 400, e.Error())
				return
			}
			d.Host = p.access.Domain
			p.Summary = append(p.Summary, "Dominio: "+d.Host)
			if d.ID != "" {
				p.Warnings = append(p.Warnings, "El dominio anterior de esta ruta dejará de abrir la aplicación. Los otros dominios se conservarán.")
			}
		}
	}
	if !edit.nameChanged && !edit.envChanged && !edit.domainChanged {
		fail(w, 400, "No hay cambios para guardar")
		return
	}
	if c.kind == "compose-file" && (edit.envChanged || edit.domainChanged) {
		if e = prepareComposeEdit(p); e != nil {
			fail(w, 400, e.Error())
			return
		}
		p.Warnings = append(p.Warnings, "Se recrearán los servicios modificados con las imágenes actuales. Puede haber una interrupción breve. Se conservarán los volúmenes y una copia de la configuración anterior.")
	} else if c.CanEdit && (edit.envChanged || edit.domainChanged) {
		if c.kind == "compose" {
			p.Warnings = append(p.Warnings, "Dokploy volverá a desplegar este proyecto para aplicar los cambios. Puede reconstruir imágenes y causar una interrupción; sus opciones de despliegue se conservarán.")
		} else if edit.envChanged {
			p.Warnings = append(p.Warnings, "Dokploy recargará la aplicación con su imagen actual para aplicar las variables. Puede haber una interrupción breve.")
		}
	} else {
		p.Summary = append(p.Summary, "El nombre visible cambia sin renombrar contenedores, redes ni volúmenes.")
	}
	sort.Strings(p.EnvironmentKeys)
	a.savePlan(w, r, p)
}
func (a *App) runApplicationEdit(ctx context.Context, p *operationPlan) error {
	e := p.edit
	current, err := a.loadApplicationConfiguration(ctx, p.server, e.before.ID)
	if err != nil || current.Revision != e.before.Revision {
		return errors.New("La configuración cambió desde la revisión. No se aplicó el cambio; abre de nuevo el editor")
	}
	if err = a.recheckAccess(ctx, p); err != nil {
		return err
	}
	if current.CanEdit && current.kind != "compose-file" {
		if err = a.applyDokployEdit(ctx, p); err != nil {
			return err
		}
	} else if e.envChanged || e.domainChanged {
		if err = a.applyComposeEdit(ctx, p); err != nil {
			return err
		}
	}
	if e.nameChanged {
		if _, err = a.db.Exec("INSERT INTO application_names(app_id,server_id,name) VALUES(?,?,?) ON CONFLICT(app_id) DO UPDATE SET name=excluded.name", current.ID, p.server.ID, e.input.Name); err != nil {
			return errors.New("Los cambios remotos terminaron, pero no se pudo guardar el nombre en Nodo. Vuelve a abrir el editor")
		}
	}
	if e.result == "" {
		e.result = "El nombre ya aparece en el inventario; no fue necesario reiniciar."
	}
	return nil
}
