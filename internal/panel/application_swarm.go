package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type editableSwarm struct {
	Service     swarmService
	RouteFile   string
	RouteSource []byte
}

func swarmContainer(spec map[string]any) map[string]any {
	return object(object(spec, "TaskTemplate"), "ContainerSpec")
}

func (a *App) loadSwarmConfiguration(ctx context.Context, s Server, c *applicationConfiguration, name string) error {
	service, err := a.inspectSwarm(ctx, s, name)
	if err != nil {
		return err
	}
	cs := swarmContainer(service.Spec)
	if cs == nil {
		return errors.New("El servicio no tiene una definición de contenedor compatible")
	}
	c.Manager, c.kind, c.appName = "Docker Swarm · Nodes", "swarm-native", name
	c.swarm = &editableSwarm{Service: service}
	c.CanEdit, c.ConnectionRequired, c.EnvironmentMode = true, false, "services"
	variables := []envValue{}
	for _, raw := range array(cs, "Env") {
		value, ok := raw.(string)
		if !ok {
			return errors.New("Docker devolvió variables no compatibles")
		}
		key, text, found := strings.Cut(value, "=")
		if !found || !envNamePattern.MatchString(key) {
			return errors.New("El servicio contiene variables que requieren revisar su definición original")
		}
		variables = append(variables, envValue{Name: key, Value: text})
	}
	sort.Slice(variables, func(i, j int) bool { return variables[i].Name < variables[j].Name })
	c.Environment = []configurationService{{Name: name, Variables: variables}}
	for _, scope := range []string{"task", "service"} {
		labels := object(cs, "Labels")
		if scope == "service" {
			labels = object(service.Spec, "Labels")
		}
		addSwarmLabelDomains(c, name, scope, labels)
	}
	// Only the service's conventional routing file is read. Never read middleware
	// credentials or modify global Traefik settings.
	base := "/etc/dokploy/traefik/dynamic/" + name
	command := "for f in " + shellQuote(base+".yml") + " " + shellQuote(base+".yaml") + `; do
 if [ -f "$f" ]; then test ! -L "$f" && test "$(wc -c < "$f")" -lt 524288 || exit 1; printf '%s\036' "$f"; cat -- "$f"; exit; fi
done`
	raw, routeErr := a.run(ctx, s, command)
	if routeErr != nil {
		return errors.New("No se pudo comprobar la ruta original de Traefik; revisa permisos SSH antes de editar")
	}
	if raw != "" {
		file, source, ok := strings.Cut(raw, "\x1e")
		if !ok || (file != base+".yml" && file != base+".yaml") {
			return errors.New("Respuesta de ruta pública inválida")
		}
		c.swarm.RouteFile, c.swarm.RouteSource = file, []byte(source)
		if err = addSwarmFileDomains(c, name, []byte(source)); err != nil {
			return err
		}
	}
	sort.Slice(c.Domains, func(i, j int) bool { return c.Domains[i].ID < c.Domains[j].ID })
	c.Message = "Nodes lee y guarda directamente por SSH y Docker, sin clave API de Dokploy. Las variables nuevas o modificadas se aplican al servicio; se conservan imagen, redes y volúmenes. No redespliegues desde otro administrador porque podría sobrescribir estos cambios."
	return nil
}

func addSwarmLabelDomains(c *applicationConfiguration, name, scope string, labels map[string]any) {
	for key, raw := range labels {
		rule, ok := raw.(string)
		if !ok || !strings.HasPrefix(key, "traefik.http.routers.") || !strings.HasSuffix(key, ".rule") {
			continue
		}
		links := ruleLinks(rule, true, "")
		if len(links) != 1 {
			continue
		}
		host := strings.TrimPrefix(links[0].URL, "https://")
		// Complex rules remain untouched; only literal single-host routes are editable.
		if !validDomain(host) || !simpleHostRule(rule, host) {
			continue
		}
		base := strings.TrimSuffix(key, ".rule")
		port, _ := strconv.Atoi(stringValue(labels, "traefik.http.services."+stringValue(labels, base+".service")+".loadbalancer.server.port"))
		if port < 1 || port > 65535 {
			continue
		}
		secure := stringValue(labels, base+".tls") == "true" || stringValue(labels, base+".tls.certresolver") != ""
		addNativeDomain(c, editableDomain{ID: scope + "|" + host, Host: host, Service: name, Port: port, HTTPS: secure, Path: "/", raw: map[string]any{"scope": scope}})
	}
}
func simpleHostRule(rule, host string) bool {
	return rule == "Host(`"+host+"`)" || rule == "Host(\""+host+"\")"
}
func addNativeDomain(c *applicationConfiguration, d editableDomain) {
	for i := range c.Domains {
		if c.Domains[i].ID == d.ID {
			if d.HTTPS {
				c.Domains[i].HTTPS = true
			}
			return
		}
	}
	c.Domains = append(c.Domains, d)
}
func nativeRouteDocument(raw []byte) (map[string]any, error) {
	var doc map[string]any
	if yaml.Unmarshal(raw, &doc) != nil || object(doc, "http") == nil {
		return nil, errors.New("La ruta pública de Traefik no se pudo interpretar")
	}
	return doc, nil
}
func addSwarmFileDomains(c *applicationConfiguration, name string, raw []byte) error {
	doc, err := nativeRouteDocument(raw)
	if err != nil {
		return err
	}
	http := object(doc, "http")
	for _, value := range object(http, "routers") {
		router, _ := value.(map[string]any)
		backend := object(object(http, "services"), strings.TrimSuffix(stringValue(router, "service"), "@file"))
		targets := array(object(backend, "loadBalancer"), "servers")
		if len(targets) != 1 {
			continue
		}
		target, _ := targets[0].(map[string]any)
		u, e := url.Parse(stringValue(target, "url"))
		if e != nil || u.Hostname() != name || u.Scheme != "http" || u.Path != "" {
			continue
		}
		port, e := strconv.Atoi(u.Port())
		if e != nil || port < 1 || port > 65535 {
			continue
		}
		rule := stringValue(router, "rule")
		links := ruleLinks(rule, true, "")
		if len(links) != 1 {
			continue
		}
		host := strings.TrimPrefix(links[0].URL, "https://")
		if validDomain(host) && simpleHostRule(rule, host) {
			addNativeDomain(c, editableDomain{ID: "file|" + host, Host: host, Service: name, Port: port, HTTPS: router["tls"] != nil, Path: "/", raw: map[string]any{"scope": "file"}})
		}
	}
	return nil
}

func prepareSwarmEdit(p *operationPlan) error {
	e := p.edit
	if e.before.swarm == nil {
		return errors.New("No se dispone de la definición Swarm actual")
	}
	e.swarmSpec = cloneObject(e.before.swarm.Service.Spec)
	cs := swarmContainer(e.swarmSpec)
	for _, change := range e.input.Environment {
		if change.Name != e.before.appName {
			return errors.New("Servicio Swarm desconocido")
		}
		env := map[string]string{}
		for _, v := range array(cs, "Env") {
			k, value, _ := strings.Cut(fmt.Sprint(v), "=")
			env[k] = value
		}
		for _, v := range change.Variables {
			env[v.Name] = v.Value
		}
		names := make([]string, 0, len(env))
		for k := range env {
			names = append(names, k)
		}
		sort.Strings(names)
		values := []any{}
		for _, k := range names {
			values = append(values, k+"="+env[k])
		}
		cs["Env"] = values
	}
	if !e.domainChanged {
		return nil
	}
	d := e.input.Domain
	if d.ID != "" {
		var old editableDomain
		for _, v := range e.before.Domains {
			if v.ID == d.ID {
				old = v
			}
		}
		scope := stringValue(old.raw, "scope")
		if scope == "file" {
			doc, err := nativeRouteDocument(e.before.swarm.RouteSource)
			if err != nil {
				return err
			}
			changed := false
			for _, value := range object(object(doc, "http"), "routers") {
				router, _ := value.(map[string]any)
				backend := object(object(object(doc, "http"), "services"), strings.TrimSuffix(stringValue(router, "service"), "@file"))
				targets := array(object(backend, "loadBalancer"), "servers")
				belongs := false
				if len(targets) == 1 {
					target, _ := targets[0].(map[string]any)
					u, parseErr := url.Parse(stringValue(target, "url"))
					belongs = parseErr == nil && u.Hostname() == e.before.appName && u.Scheme == "http"
				}
				if belongs && simpleHostRule(stringValue(router, "rule"), old.Host) {
					router["rule"] = "Host(`" + d.Host + "`)"
					changed = true
				}
			}
			if !changed {
				return errors.New("La ruta pública cambió; vuelve a abrir el editor")
			}
			e.routeBytes, err = yaml.Marshal(doc)
			return err
		}
		labels := object(cs, "Labels")
		if scope == "service" {
			labels = object(e.swarmSpec, "Labels")
		}
		changed := false
		for key, value := range labels {
			rule, ok := value.(string)
			if ok && strings.HasPrefix(key, "traefik.http.routers.") && strings.HasSuffix(key, ".rule") && simpleHostRule(rule, old.Host) {
				labels[key] = "Host(`" + d.Host + "`)"
				changed = true
			}
		}
		if !changed {
			return errors.New("La ruta pública no es compatible con edición directa")
		}
		return nil
	}
	if p.access == nil {
		return errors.New("No se revisó el acceso público")
	}
	// Docker's task provider sees only local tasks. Require a single-node manager,
	// as in native deployments; a distributed cluster needs a Swarm proxy provider.
	router := "nodo-edit-" + p.ID[:12]
	base := "traefik.http.routers." + router
	labels := object(cs, "Labels")
	if labels == nil {
		labels = map[string]any{}
		cs["Labels"] = labels
	}
	if network := stringValue(labels, "traefik.docker.network"); network != "" && network != p.access.Proxy.Network {
		return errors.New("El servicio utiliza otra red de proxy; conserva su configuración original")
	}
	labels["traefik.enable"] = "true"
	labels["lat.nodo.managed"] = "true"
	labels["traefik.docker.network"] = p.access.Proxy.Network
	labels[base+".rule"] = "Host(`" + d.Host + "`)"
	labels[base+".service"] = router
	labels[base+".entrypoints"] = p.access.Proxy.HTTPEntry
	labels["traefik.http.services."+router+".loadbalancer.server.port"] = strconv.Itoa(d.Port)
	if d.HTTPS {
		labels[base+".entrypoints"] = p.access.Proxy.HTTPSEntry
		labels[base+".tls"] = "true"
		labels[base+".tls.certresolver"] = p.access.Proxy.Resolver
		labels[base+"-http.rule"] = labels[base+".rule"]
		labels[base+"-http.entrypoints"] = p.access.Proxy.HTTPEntry
		labels[base+"-http.service"] = router
		labels[base+"-http.middlewares"] = router + "-https"
		labels["traefik.http.middlewares."+router+"-https.redirectscheme.scheme"] = "https"
	}
	return nil
}

func (a *App) saveSwarmEditRecord(p *operationPlan, spec map[string]any, stage string) error {
	raw, err := json.Marshal(map[string]any{"spec": spec, "previousSpec": p.edit.before.swarm.Service.Spec, "previousRoute": p.edit.before.swarm.RouteSource, "routeFile": p.edit.before.swarm.RouteFile})
	if err != nil {
		return err
	}
	defer clear(raw)
	protected, encrypted, err := sealDokployKey(p.server, raw)
	if err != nil {
		return errors.New("No se pudo proteger el respaldo; no se aplicarán cambios")
	}
	_, err = a.db.Exec("INSERT INTO native_swarm_apps VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(server_id,service_name) DO UPDATE SET operation_id=excluded.operation_id,connection_hash=excluded.connection_hash,protected_key=excluded.protected_key,definition=excluded.definition,stage=excluded.stage,updated_at=excluded.updated_at", p.server.ID, p.edit.before.appName, p.ID, connectionHash(p.server), protected, encrypted, stage, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (a *App) waitSwarmEdit(ctx context.Context, s Server, name string, expected map[string]any) error {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	for {
		current, err := a.inspectSwarm(ctx, s, name)
		if err != nil {
			return err
		}
		if specHash(current.Spec) != specHash(expected) {
			return errors.New("Docker revirtió la definición o fue modificada por otro administrador")
		}
		raw, err := a.run(ctx, s, "docker service inspect --format '{{json .UpdateStatus}}' "+shellQuote(name))
		if err != nil {
			return errors.New("No se pudo comprobar la actualización Swarm")
		}
		var status struct{ State string }
		if json.Unmarshal([]byte(raw), &status) != nil {
			return errors.New("Estado de actualización Swarm inválido")
		}
		if status.State == "paused" || strings.HasPrefix(status.State, "rollback") {
			return errors.New("Docker no pudo completar la actualización del servicio")
		}
		if status.State == "" || status.State == "completed" {
			ready, taskErr := a.swarmEditTasksReady(ctx, s, name, expected)
			if taskErr != nil {
				return taskErr
			}
			if ready {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("La actualización Swarm no terminó a tiempo")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (a *App) writeSwarmRoute(ctx context.Context, p *operationPlan) error {
	c := p.edit.before.swarm
	file := shellQuote(c.RouteFile)
	tmp := shellQuote(c.RouteFile + ".nodo-" + p.ID[:16])
	check := `test -f ` + file + ` && test ! -L ` + file + ` && test "$(sha256sum -- ` + file + ` | cut -d ' ' -f 1)" = ` + shellQuote(hashBytes(c.RouteSource))
	command := "set -eu; umask 077; " + check + "; test ! -e " + tmp + "; (set -C; cat > " + tmp + "); trap " + shellQuote("rm -f -- "+tmp) + " EXIT; chmod \"$(stat -c '%a' " + file + ")\" " + tmp + "; chown \"$(stat -c '%u:%g' " + file + ")\" " + tmp + "; " + check + "; mv -- " + tmp + " " + file + "; printf APPLIED"
	out := &cappedBuffer{limit: 100}
	_ = a.stream(ctx, p.server, command, strings.NewReader(string(p.edit.routeBytes)), out)
	if !a.swarmRouteMatches(ctx, p.server, c.RouteFile, p.edit.routeBytes) {
		return errors.New("No se confirmó el archivo de ruta. Comprueba el dominio y Actividad antes de reintentar")
	}
	return nil
}

func (a *App) applySwarmEdit(ctx context.Context, p *operationPlan) error {
	e := p.edit
	before := e.before.swarm.Service
	current, err := a.inspectSwarm(ctx, p.server, e.before.appName)
	if err != nil || current.ID != before.ID || current.Version.Index != before.Version.Index || specHash(current.Spec) != specHash(before.Spec) {
		return errors.New("El servicio cambió después de la revisión; no se aplicaron cambios")
	}
	if e.input.Domain != nil && e.input.Domain.ID == "" && e.domainChanged {
		raw, checkErr := a.run(ctx, p.server, swarmTopologyCommand)
		var topology struct {
			State   string
			Control bool
			Nodes   int
		}
		if checkErr != nil || json.Unmarshal([]byte(raw), &topology) != nil || !topology.Control || topology.Nodes != 1 {
			return errors.New("Añadir una ruta por tareas Docker requiere un Swarm de un solo nodo")
		}
		// Do not alter network membership implicitly; the target network must exist.
		raw, checkErr = a.run(ctx, p.server, "docker network inspect --format '{{json .Id}}' "+shellQuote(p.access.Proxy.Network))
		var networkID string
		if checkErr != nil || json.Unmarshal([]byte(raw), &networkID) != nil {
			return errors.New("No se pudo comprobar la red del proxy")
		}
		connected := false
		for _, v := range swarmNetworks(current.Spec) {
			n, _ := v.(map[string]any)
			if stringValue(n, "Target") == networkID || stringValue(n, "Target") == p.access.Proxy.Network {
				connected = true
			}
		}
		if !connected {
			return errors.New("El servicio no está conectado a la red del proxy; añade esa red antes de crear el dominio")
		}
	}
	if err = a.saveSwarmEditRecord(p, before.Spec, "edit-prepared"); err != nil {
		return err
	}
	changed := specHash(before.Spec) != specHash(e.swarmSpec)
	if changed {
		err = a.updateSwarm(ctx, p.server, current, e.swarmSpec)
		if err == nil {
			err = a.waitSwarmEdit(ctx, p.server, e.before.appName, e.swarmSpec)
		}
	}
	if err == nil && len(e.routeBytes) > 0 {
		err = a.writeSwarmRoute(ctx, p)
	}
	if err != nil {
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 130*time.Second)
		defer cancel()
		current, readErr := a.inspectSwarm(recovery, p.server, e.before.appName)
		routeSafe := len(e.routeBytes) == 0 || a.swarmRouteMatches(recovery, p.server, e.before.swarm.RouteFile, e.before.swarm.RouteSource)
		if routeSafe && readErr == nil && current.ID == before.ID && specHash(current.Spec) == specHash(before.Spec) {
			_ = a.saveSwarmEditRecord(p, before.Spec, "active")
			return err
		}
		if routeSafe && readErr == nil && current.ID == before.ID && specHash(current.Spec) == specHash(e.swarmSpec) {
			if a.updateSwarm(recovery, p.server, current, before.Spec) == nil && a.waitSwarmEdit(recovery, p.server, e.before.appName, before.Spec) == nil {
				_ = a.saveSwarmEditRecord(p, before.Spec, "active")
				return errors.New("No se completó el cambio. Se restauró la definición anterior del servicio")
			}
		}
		_ = a.saveSwarmEditRecord(p, before.Spec, "edit-recovery-required")
		return errors.New("No se pudo confirmar o recuperar toda la actualización. Se conserva un respaldo cifrado; revisa Actividad y el servicio antes de reintentar")
	}
	if err = a.saveSwarmEditRecord(p, e.swarmSpec, "active"); err != nil {
		return errors.New("Docker recibió los cambios, pero no se pudo actualizar el respaldo cifrado. Revisa el servicio antes de reintentar")
	}
	e.result = "Nodes confirmó la actualización directa en Docker Swarm. Se conservaron imagen, volúmenes y demás opciones. La emisión de HTTPS depende del proxy."
	return nil
}

func (a *App) swarmRouteMatches(ctx context.Context, s Server, file string, source []byte) bool {
	raw, err := a.run(ctx, s, "test -f "+shellQuote(file)+" && test ! -L "+shellQuote(file)+" && sha256sum -- "+shellQuote(file))
	parts := strings.Fields(raw)
	return err == nil && len(parts) > 0 && parts[0] == hashBytes(source)
}

func (a *App) swarmEditTasksReady(ctx context.Context, s Server, name string, spec map[string]any) (bool, error) {
	command := "ids=$(docker service ps -q --filter desired-state=running " + shellQuote(name) + ") || exit 1; if [ -n \"$ids\" ]; then docker inspect --type task --format '{\"State\":{{json .Status.State}},\"ContainerSpec\":{{json .Spec.ContainerSpec}}}' $ids; fi"
	raw, err := a.run(ctx, s, command)
	if err != nil {
		return false, errors.New("No se pudo comprobar las tareas actualizadas")
	}
	expected := swarmContainer(spec)
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var task struct {
			State         string
			ContainerSpec map[string]any
		}
		if json.Unmarshal([]byte(line), &task) != nil {
			return false, errors.New("Estado de tarea inválido")
		}
		if task.State != "running" {
			return false, nil
		}
		for _, key := range []string{"Image", "Env", "Labels"} {
			left, _ := json.Marshal(task.ContainerSpec[key])
			right, _ := json.Marshal(expected[key])
			if string(left) != string(right) {
				return false, nil
			}
		}
		count++
	}
	replicas := object(object(spec, "Mode"), "Replicated")
	if replicas != nil {
		desired, ok := replicas["Replicas"].(float64)
		return ok && count == int(desired), nil
	}
	return count > 0, nil
}
