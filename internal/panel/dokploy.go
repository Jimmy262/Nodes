package panel

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func connectionHash(s Server) string {
	b, _ := json.Marshal([]any{s.ID, s.Host, s.Port, s.User, s.Identity})
	return hashBytes(b)
}
func sealDokployKey(s Server, key []byte) ([]byte, []byte, error) {
	secret := make([]byte, 32)
	if _, e := rand.Read(secret); e != nil {
		return nil, nil, e
	}
	defer clear(secret)
	protected, e := protectSecret(secret)
	if e != nil {
		return nil, nil, e
	}
	block, e := aes.NewCipher(secret)
	if e != nil {
		return nil, nil, e
	}
	gcm, e := cipher.NewGCM(block)
	if e != nil {
		return nil, nil, e
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, nil, e
	}
	return protected, gcm.Seal(nonce, nonce, key, []byte(connectionHash(s))), nil
}
func (a *App) dokployKey(s Server) ([]byte, error) {
	var binding string
	var protected, encrypted []byte
	if e := a.db.QueryRow("SELECT connection_hash,protected_key,secret FROM dokploy_connections WHERE server_id=?", s.ID).Scan(&binding, &protected, &encrypted); e != nil {
		return nil, e
	}
	if binding != connectionHash(s) {
		return nil, errors.New("La conexión SSH cambió; vuelve a conectar Dokploy")
	}
	return openConnectionSecret(s, protected, encrypted)
}
func openConnectionSecret(s Server, protected, encrypted []byte) ([]byte, error) {
	binding := connectionHash(s)
	secret, e := unprotectSecret(protected)
	if e != nil {
		return nil, e
	}
	defer clear(secret)
	block, e := aes.NewCipher(secret)
	if e != nil {
		return nil, e
	}
	gcm, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	if len(encrypted) < gcm.NonceSize() {
		return nil, errors.New("Clave protegida inválida")
	}
	return gcm.Open(nil, encrypted[:gcm.NonceSize()], encrypted[gcm.NonceSize():], []byte(binding))
}

// The API key travels through SSH stdin, never command arguments or local logs.
// The destination is the existing Dokploy instance on the selected VPS only.
func (a *App) dokployAPI(ctx context.Context, s Server, key []byte, method, endpoint string, body any, out any) error {
	if method != "GET" && method != "POST" {
		return errors.New("Método Dokploy inválido")
	}
	if strings.ContainsAny(endpoint, "\r\n#") || strings.HasPrefix(endpoint, "/") {
		return errors.New("Ruta Dokploy inválida")
	}
	config := "url = " + strconv.Quote("http://127.0.0.1:3000/api/"+endpoint) + "\nrequest = " + strconv.Quote(method) + "\nheader = " + strconv.Quote("x-api-key: "+string(key)) + "\nheader = \"Content-Type: application/json\"\n"
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		config += "data-binary = " + strconv.Quote(string(b)) + "\n"
	}
	buffer := &cappedBuffer{limit: 4 << 20}
	command := `curl -q --silent --show-error --max-time 180 --max-filesize 4194000 --noproxy '*' --config - --write-out '\n%{http_code}'`
	if e := a.stream(ctx, s, command, strings.NewReader(config), buffer); e != nil {
		return errors.New("No se pudo consultar Dokploy en este servidor. Comprueba que escucha en el puerto 3000 y vuelve a intentarlo")
	}
	data := string(buffer.data)
	index := strings.LastIndex(data, "\n")
	if index < 0 {
		return errors.New("Dokploy devolvió una respuesta incompleta")
	}
	status, e := strconv.Atoi(strings.TrimSpace(data[index+1:]))
	if e != nil {
		return errors.New("Respuesta Dokploy inválida")
	}
	if status == 401 || status == 403 {
		return errors.New("Dokploy rechazó la clave o sus permisos. Conecta una clave con acceso a esta aplicación, sus variables, dominios y despliegues")
	}
	if status == 404 {
		return errors.New("Esta versión de Dokploy no ofrece la operación solicitada, o el proyecto ya no existe")
	}
	if status < 200 || status >= 300 {
		return errors.New("Dokploy no completó la operación. Revisa su estado antes de reintentar")
	}
	if out != nil && json.Unmarshal([]byte(data[:index]), out) != nil {
		return errors.New("No se pudo interpretar la respuesta de Dokploy")
	}
	return nil
}
func (a *App) connectDokploy(w http.ResponseWriter, r *http.Request) {
	s, e := a.server(r)
	if e != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	var in struct {
		Key string `json:"key"`
	}
	if decode(w, r, &in) != nil {
		fail(w, 400, "Clave inválida")
		return
	}
	in.Key = strings.TrimSpace(in.Key)
	if len(in.Key) < 16 || len(in.Key) > 2048 {
		fail(w, 400, "Pega la clave API generada en Dokploy")
		return
	}
	for _, r := range in.Key {
		if r < 33 || r > 126 {
			fail(w, 400, "La clave contiene caracteres inválidos")
			return
		}
	}
	key := []byte(in.Key)
	defer clear(key)
	var check any
	if e = a.dokployAPI(r.Context(), s, key, "GET", "project.all", nil, &check); e != nil {
		fail(w, 502, e.Error())
		return
	}
	protected, encrypted, e := sealDokployKey(s, key)
	if e != nil {
		fail(w, 500, "Windows no pudo proteger la clave. No se guardó")
		return
	}
	_, e = a.db.Exec("INSERT INTO dokploy_connections VALUES(?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET connection_hash=excluded.connection_hash,protected_key=excluded.protected_key,secret=excluded.secret", s.ID, connectionHash(s), protected, encrypted)
	if e != nil {
		fail(w, 500, "No se pudo guardar la conexión")
		return
	}
	reply(w, 200, map[string]bool{"connected": true})
}
func stringValue(m map[string]any, key string) string { v, _ := m[key].(string); return v }
func (a *App) loadDokployConfiguration(ctx context.Context, s Server, key []byte, c *applicationConfiguration) error {
	var search struct {
		Items []map[string]any `json:"items"`
	}
	if e := a.dokployAPI(ctx, s, key, "GET", c.kind+".search?appName="+url.QueryEscape(c.appName)+"&limit=100", nil, &search); e != nil {
		return e
	}
	for _, item := range search.Items {
		if stringValue(item, "appName") == c.appName {
			if c.recordID != "" {
				return errors.New("Hay más de un proyecto Dokploy con este identificador")
			}
			c.recordID = stringValue(item, c.kind+"Id")
		}
	}
	if c.recordID == "" {
		return errors.New("No se encontró esta aplicación en el Dokploy del servidor. Comprueba la cuenta y los permisos de la clave API")
	}
	if e := a.dokployAPI(ctx, s, key, "GET", c.kind+".one?"+c.kind+"Id="+url.QueryEscape(c.recordID), nil, &c.record); e != nil {
		return e
	}
	if stringValue(c.record, "appName") != c.appName {
		return errors.New("La aplicación Dokploy ya no coincide con el contenedor")
	}
	if name := stringValue(c.record, "name"); name != "" {
		c.Name = name
	}
	if remote := stringValue(c.record, "serverId"); remote != "" {
		var server map[string]any
		if e := a.dokployAPI(ctx, s, key, "GET", "server.one?serverId="+url.QueryEscape(remote), nil, &server); e != nil {
			return e
		}
		if c.PublicIP == "" || stringValue(server, "ipAddress") != c.PublicIP {
			return errors.New("Dokploy administra este proyecto en otra VPS. Conéctalo desde su servidor de destino")
		}
	}
	c.EnvText = stringValue(c.record, "env")
	c.EnvironmentMode = "dotenv"
	c.CanEdit = true
	c.Message = "Los cambios se guardan también en Dokploy. Las variables del proyecto se aplican como indica su Compose; las variables de compilación se conservan."
	if c.kind == "application" {
		c.Message = "Los cambios se guardan también en Dokploy. Las variables de compilación y secretos de construcción se conservan."
	}
	var domains []map[string]any
	endpoint := "domain.byApplicationId?applicationId="
	if c.kind == "compose" {
		endpoint = "domain.byComposeId?composeId="
	}
	if e := a.dokployAPI(ctx, s, key, "GET", endpoint+url.QueryEscape(c.recordID), nil, &domains); e != nil {
		return e
	}
	for _, d := range domains {
		if enabled, ok := d["enabled"].(bool); ok && !enabled {
			continue
		}
		port, _ := d["port"].(float64)
		secure, _ := d["https"].(bool)
		c.Domains = append(c.Domains, editableDomain{ID: stringValue(d, "domainId"), Host: stringValue(d, "host"), Service: stringValue(d, "serviceName"), Port: int(port), HTTPS: secure, Path: stringValue(d, "path"), raw: d})
	}
	sort.Slice(c.Domains, func(i, j int) bool { return c.Domains[i].ID < c.Domains[j].ID })
	return nil
}
func dokployDomainBody(d editableDomain) map[string]any {
	body := map[string]any{"domainId": d.ID, "host": d.Host}
	for _, key := range []string{"path", "port", "customEntrypoint", "https", "certificateType", "customCertResolver", "serviceName", "domainType", "internalPath", "stripPath", "middlewares", "forwardAuthEnabled", "enabled"} {
		if value, ok := d.raw[key]; ok {
			body[key] = value
		}
	}
	return body
}
func (a *App) applyDokployEdit(ctx context.Context, p *operationPlan) error {
	e := p.edit
	c := e.before
	key, err := a.dokployKey(p.server)
	if err != nil {
		return errors.New("Vuelve a conectar la clave API de Dokploy")
	}
	defer clear(key)
	apply := func(endpoint string, body any) error {
		return a.dokployAPI(ctx, p.server, key, "POST", endpoint, body, nil)
	}
	// Verify that deployment status can be read before changing persistent settings.
	if c.kind == "application" && (e.envChanged || e.domainChanged) && stringValue(c.record, "applicationStatus") == "running" {
		return errors.New("Dokploy ya está desplegando esta aplicación. Espera a que termine")
	}
	if c.kind == "compose" && (e.envChanged || e.domainChanged) {
		if c.record["isolatedDeploymentsVolume"] == true {
			return errors.New("Este proyecto crea volúmenes nuevos en cada despliegue. Ajusta esa opción en Dokploy antes de aplicar desde Nodo")
		}
		if stringValue(c.record, "composeStatus") == "running" {
			return errors.New("Dokploy ya está desplegando este proyecto. Espera a que termine")
		}
		var check any
		if err = a.dokployAPI(ctx, p.server, key, "GET", "deployment.allByCompose?composeId="+url.QueryEscape(c.recordID), nil, &check); err != nil {
			return err
		}
	}
	a.operationState(p, "running", "Guardando la configuración en Dokploy…")
	failed := func() error {
		return errors.New("Dokploy no completó todos los pasos. Algunos cambios pueden estar guardados; abre de nuevo el editor para ver el estado actual antes de reintentar")
	}
	if e.nameChanged || e.envChanged {
		body := map[string]any{c.kind + "Id": c.recordID}
		if e.nameChanged {
			body["name"] = e.input.Name
		}
		if e.envChanged {
			body["env"] = *e.input.EnvText
		}
		if err = apply(c.kind+".update", body); err != nil {
			return failed()
		}
		if e.nameChanged {
			if _, err = a.db.Exec("INSERT INTO application_names(app_id,server_id,name) VALUES(?,?,?) ON CONFLICT(app_id) DO UPDATE SET name=excluded.name", c.ID, p.server.ID, e.input.Name); err != nil {
				return failed()
			}
		}
	}
	if e.domainChanged {
		d := e.input.Domain
		if d.ID != "" {
			var old editableDomain
			for _, item := range c.Domains {
				if item.ID == d.ID {
					old = item
				}
			}
			body := dokployDomainBody(old)
			body["host"] = d.Host
			if err = apply("domain.update", body); err != nil {
				return failed()
			}
		} else {
			certificate := "none"
			if d.HTTPS {
				certificate = "letsencrypt"
			}
			body := map[string]any{c.kind + "Id": c.recordID, "host": d.Host, "port": d.Port, "path": "/", "https": d.HTTPS, "certificateType": certificate, "domainType": c.kind}
			if c.kind == "compose" {
				body["serviceName"] = d.Service
			}
			if err = apply("domain.create", body); err != nil {
				return failed()
			}
		}
	}
	if c.kind == "compose" && (e.envChanged || e.domainChanged) {
		title := "Nodo configuración " + p.ID
		if err = apply("compose.redeploy", map[string]any{"composeId": c.recordID, "title": title, "freshVolumes": false}); err != nil {
			return failed()
		}
		a.operationState(p, "running", "Configuración guardada. Dokploy está aplicando el despliegue…")
		for {
			var deployments []struct {
				Title  string `json:"title"`
				Status string `json:"status"`
			}
			if err = a.dokployAPI(ctx, p.server, key, "GET", "deployment.allByCompose?composeId="+url.QueryEscape(c.recordID), nil, &deployments); err != nil {
				return errors.New("La configuración se guardó y el despliegue se solicitó, pero no se pudo consultar su resultado. Revisa Dokploy antes de reintentar")
			}
			for _, d := range deployments {
				if d.Title != title {
					continue
				}
				if d.Status == "done" {
					e.result = "Dokploy confirmó el despliegue actualizado."
					return nil
				}
				if d.Status == "error" {
					return errors.New("La configuración quedó guardada, pero Dokploy informó un error al desplegar. Revisa sus registros antes de volver a aplicar")
				}
			}
			select {
			case <-ctx.Done():
				return errors.New("La configuración se guardó y Dokploy sigue gestionando el despliegue. Comprueba su resultado antes de reintentar")
			case <-time.After(5 * time.Second):
			}
		}
	}
	if c.kind == "application" && e.envChanged {
		a.operationState(p, "running", "Configuración guardada. Recargando la aplicación con Dokploy…")
		if err = apply("application.reload", map[string]any{"applicationId": c.recordID, "appName": c.appName}); err != nil {
			return errors.New("La configuración quedó guardada, pero Dokploy no confirmó la recarga. Revisa la aplicación antes de reintentar")
		}
		e.result = "Dokploy confirmó la recarga. Comprueba la aplicación."
	} else {
		e.result = "Los cambios también quedaron guardados en Dokploy."
	}
	return nil
}
