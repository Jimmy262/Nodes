package panel

import (
	"context"
	"encoding/json"
	"errors"
	"go.yaml.in/yaml/v3"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const swarmTopologyCommand = `docker info --format '{"State":{{json .Swarm.LocalNodeState}},"Control":{{json .Swarm.ControlAvailable}},"Nodes":{{json .Swarm.Nodes}},"Cluster":{{if .Swarm.Cluster}}{{json .Swarm.Cluster.ID}}{{else}}""{{end}}}'`
const swarmVolumesCommand = `ids=$(docker service ls -q) || exit 1; if [ -n "$ids" ]; then docker service inspect --format '{"Name":{{json .Spec.Name}},"Mounts":{{json .Spec.TaskTemplate.ContainerSpec.Mounts}}}' $ids; fi`

var swarmNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

type swarmService struct {
	ID      string
	Version struct{ Index uint64 }
	Spec    map[string]any
}
type swarmMigration struct {
	Source          swarmService
	TargetNetwork   string
	Proxy           proxyOptions
	Domain          string
	Port            int
	SourceRouteHash string
	Mounts          []swarmVolume
}
type swarmVolume struct {
	Source, Target string
	ReadOnly       bool
}

func (a *App) checkSwarmDNS(ctx context.Context, host, ip string) domainStatus {
	if a.swarmDNS != nil {
		return a.swarmDNS(ctx, host, ip)
	}
	return checkDomainDNS(ctx, host, ip)
}

func object(m map[string]any, key string) map[string]any { v, _ := m[key].(map[string]any); return v }
func array(m map[string]any, key string) []any           { v, _ := m[key].([]any); return v }
func cloneObject(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var v map[string]any
	json.Unmarshal(b, &v)
	return v
}
func specHash(m map[string]any) string { b, _ := json.Marshal(m); return hashBytes(b) }
func dockerAPIMinor(version string) (int, error) {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) != 2 || parts[0] != "1" {
		return 0, errors.New("Versión API Docker inválida")
	}
	minor, e := strconv.Atoi(parts[1])
	if e != nil || minor < 40 {
		return 0, errors.New("La API Docker debe ser 1.40 o posterior")
	}
	return minor, nil
}
func swarmNetworks(spec map[string]any) []any {
	if v := array(object(spec, "TaskTemplate"), "Networks"); len(v) > 0 {
		return v
	}
	return array(spec, "Networks")
}
func (a *App) inspectSwarm(ctx context.Context, s Server, name string) (swarmService, error) {
	var service swarmService
	if !swarmNamePattern.MatchString(name) {
		return service, errors.New("Nombre de servicio Swarm inválido")
	}
	raw, e := a.run(ctx, s, "docker service inspect --format '{{json .}}' "+shellQuote(name))
	if e != nil || len(raw) >= 1<<20 || json.Unmarshal([]byte(raw), &service) != nil || service.ID == "" || stringValue(service.Spec, "Name") != name {
		return service, errors.New("No se pudo leer el servicio Swarm actual")
	}
	return service, nil
}
func (a *App) planManagedMigration(ctx context.Context, p *operationPlan) {
	name, _ := p.container.labels()["com.docker.swarm.service.name"].(string)
	p.Name = name
	p.Manager = "Swarm · Nodo"
	p.Summary = []string{}
	p.Blockers = []string{}
	p.Warnings = []string{}
	if p.ID == "" {
		var e error
		p.ID, e = randomToken()
		if e != nil {
			p.Blockers = append(p.Blockers, "No se pudo preparar el traslado")
			return
		}
	}
	service, e := a.inspectSwarm(ctx, p.server, name)
	if e != nil {
		p.Blockers = append(p.Blockers, e.Error())
		return
	}
	p.Summary = append(p.Summary, "Se adoptará y recreará el servicio Swarm completo: "+name+" · una réplica.", "Se guardará su definición cifrada en Nodo; no requiere API de Dokploy.", "Se conservarán variables, comando, límites, imagen y los volúmenes locales compatibles.")
	task := object(service.Spec, "TaskTemplate")
	cs := object(task, "ContainerSpec")
	if cs == nil || stringValue(cs, "Image") == "" {
		p.Blockers = append(p.Blockers, "El servicio no tiene una definición de contenedor compatible.")
	}
	replicas := object(object(service.Spec, "Mode"), "Replicated")
	if replicas == nil || replicas["Replicas"] != float64(1) {
		p.Blockers = append(p.Blockers, "Este traslado admite un servicio con una réplica activa. Las réplicas múltiples o globales requieren migrar el clúster.")
	}
	if !p.container.State.Running || p.container.State.Paused {
		p.Blockers = append(p.Blockers, "La tarea original debe estar ejecutándose para capturar su imagen y comprobar su definición.")
	}
	if len(array(cs, "Secrets")) > 0 || len(array(cs, "Configs")) > 0 {
		p.Blockers = append(p.Blockers, "Este servicio usa secretos o configs de Swarm. Hay que importarlos de forma explícita antes de trasladarlo.")
	}
	if len(array(object(task, "Placement"), "Constraints")) > 0 || len(array(object(task, "Placement"), "Preferences")) > 0 {
		p.Blockers = append(p.Blockers, "El servicio fija nodos o reglas de ubicación que deben adaptarse al destino.")
	}
	if len(array(object(service.Spec, "EndpointSpec"), "Ports")) > 0 {
		p.Blockers = append(p.Blockers, "El servicio publica puertos directamente. Este traslado admite acceso a través de Traefik para evitar conflictos de puertos.")
	}
	if driver := stringValue(object(task, "LogDriver"), "Name"); driver != "" && driver != "json-file" && driver != "local" && driver != "journald" {
		p.Blockers = append(p.Blockers, "El servicio utiliza un controlador de logs externo que necesita configurarse en el destino.")
	}
	if len(swarmNetworks(service.Spec)) != 1 || (len(array(task, "Networks")) > 0 && len(array(service.Spec, "Networks")) > 0) {
		p.Blockers = append(p.Blockers, "El servicio necesita una única red overlay compatible con el proxy.")
	}
	for _, key := range []string{"Privileges", "CredentialSpec"} {
		if v := cs[key]; v != nil {
			b, _ := json.Marshal(v)
			if string(b) != "{}" {
				p.Blockers = append(p.Blockers, "La opción "+key+" requiere un traslado específico.")
			}
		}
	}
	migration := &swarmMigration{Source: service}
	for _, raw := range array(cs, "Mounts") {
		m, _ := raw.(map[string]any)
		source, target := stringValue(m, "Source"), stringValue(m, "Target")
		ro, _ := m["ReadOnly"].(bool)
		p.Summary = append(p.Summary, "Volumen: "+source+" → "+target)
		if stringValue(m, "Type") != "volume" || !swarmNamePattern.MatchString(source) || !strings.HasPrefix(target, "/") {
			p.Blockers = append(p.Blockers, "Solo se pueden copiar volúmenes locales con nombre; los binds y montajes especiales requieren revisión.")
			continue
		}
		if options := object(m, "VolumeOptions"); options != nil {
			for key, value := range options {
				if key != "NoCopy" && value != nil {
					p.Blockers = append(p.Blockers, "Un volumen utiliza opciones especiales: "+key)
				}
			}
		}
		migration.Mounts = append(migration.Mounts, swarmVolume{source, target, ro})
	}
	if len(p.Blockers) > 0 {
		return
	}
	p.swarm = migration
	if e = a.checkSwarmResources(ctx, p); e != nil {
		p.Blockers = append(p.Blockers, e.Error())
		return
	}
	port, routeHash, e := a.swarmRoute(ctx, p.server, name)
	if e != nil {
		p.Blockers = append(p.Blockers, e.Error())
		return
	}
	migration.Port = port
	migration.SourceRouteHash = routeHash
	migration.Proxy = a.proxyOptions(ctx, p.target)
	if !migration.Proxy.Available || !migration.Proxy.HTTPS || migration.Proxy.PublicIP == "" {
		p.Blockers = append(p.Blockers, "El destino necesita Traefik, su red overlay y un resolver HTTPS compatible.")
		return
	}
	prefix := regexp.MustCompile(`[^a-z0-9-]`).ReplaceAllString(strings.ToLower(name), "-")
	if len(prefix) > 40 {
		prefix = prefix[:40]
	}
	prefix = strings.TrimRight(prefix, "-")
	migration.Domain = prefix + "-" + p.ID[:8] + "." + migration.Proxy.PublicIP + ".sslip.io"
	p.URL = "https://" + migration.Domain
	dns := a.checkSwarmDNS(ctx, migration.Domain, migration.Proxy.PublicIP)
	p.DNS = &dns
	if !dns.Ready {
		p.Blockers = append(p.Blockers, "El enlace automático aún no resuelve hacia el destino. Vuelve a revisar el traslado.")
	}
	used, e := a.domainInUse(ctx, p.target, migration.Domain)
	if e != nil {
		p.Blockers = append(p.Blockers, e.Error())
	} else if used {
		p.Blockers = append(p.Blockers, "El dominio de destino ya está ocupado.")
	}
	p.Summary = append(p.Summary, "Se detendrá la réplica original antes de copiar los datos y quedará conservada con cero réplicas.", "Nuevo enlace del destino: "+p.URL, "El destino seguirá siendo Swarm y usará la red del proxy, sin depender de Dokploy.")
	p.Warnings = append(p.Warnings, "Habrá una interrupción. Se capturará la capa de archivos actual y se copiarán los volúmenes con el servicio detenido. Máximo 10 GiB por imagen o volumen.", "El enlace anterior incluye la IP del origen y no se mueve. Los dominios propios necesitan cambiar el DNS; las variables con URLs u orígenes permitidos pueden requerir ajustes.", "Si falla la copia, se intentará detener y comprobar el destino antes de recuperar el original. Si no se puede confirmar que la copia está detenida, el original seguirá detenido para evitar dos instancias activas.", "El servicio conserva su registro original en Dokploy. No vuelvas a desplegarlo desde allí mientras esté trasladado. Nodo adopta su ejecución; no importa las opciones originales de Git o construcción.")
	p.Review = &operationReview{
		Summary: []string{"Se copiarán la imagen, las variables y los datos del servicio completo.", "El destino se administrará desde Nodo, sin la API de Dokploy."},
		Warnings: []string{
			"Habrá una interrupción. El original quedará detenido y conservado.",
			"El enlace anterior no se traslada. Revisa DNS y URLs si usas un dominio propio.",
			"Si falla y no se puede aislar el destino, el original seguirá detenido.",
			"No redespliegues este servicio desde Dokploy.",
		},
	}
}
func (a *App) checkSwarmResources(ctx context.Context, p *operationPlan) error {
	m := p.swarm
	src, e := a.daemon(ctx, p.server)
	if e != nil {
		return e
	}
	dst, e := a.daemon(ctx, p.target)
	if e != nil {
		return e
	}
	if src["id"] == dst["id"] || src["os"] != "linux" || dst["os"] != "linux" || src["arch"] != dst["arch"] {
		return errors.New("El origen y destino deben ser motores distintos, Linux y de la misma arquitectura")
	}
	clusters := []string{}
	apiVersions := []int{}
	for _, server := range []Server{p.server, p.target} {
		version, err := a.run(ctx, server, `docker version --format '{{.Server.APIVersion}}'`)
		if err != nil {
			return errors.New("No se pudo comprobar la versión de la API Docker")
		}
		number, err := dockerAPIMinor(version)
		if err != nil {
			return errors.New("La versión Docker no es compatible con el traslado nativo")
		}
		apiVersions = append(apiVersions, number)
	}
	if apiVersions[1] < apiVersions[0] {
		return errors.New("La API Docker del destino es anterior a la del origen. Actualiza el destino para conservar la definición del servicio")
	}
	for _, s := range []Server{p.server, p.target} {
		raw, e := a.run(ctx, s, swarmTopologyCommand)
		var topology struct {
			State, Cluster string
			Control        bool
			Nodes          int
		}
		if e != nil || json.Unmarshal([]byte(raw), &topology) != nil || topology.State != "active" || !topology.Control || topology.Nodes != 1 || topology.Cluster == "" {
			return errors.New("Este traslado requiere un Swarm independiente de un solo nodo manager en cada servidor")
		}
		clusters = append(clusters, topology.Cluster)
	}
	if clusters[0] == clusters[1] {
		return errors.New("Los servidores pertenecen al mismo clúster Swarm")
	}
	for i, s := range []Server{p.server, p.target} {
		raw, e := a.run(ctx, s, `docker network inspect dokploy-network --format '{"ID":{{json .Id}},"Driver":{{json .Driver}},"Scope":{{json .Scope}},"Attachable":{{json .Attachable}}}'`)
		var network struct {
			ID, Driver, Scope string
			Attachable        bool
		}
		if e != nil || json.Unmarshal([]byte(raw), &network) != nil || network.Driver != "overlay" || network.Scope != "swarm" || !network.Attachable {
			return errors.New("No se pudo comprobar una red overlay attachable compatible en ambos servidores")
		}
		if i == 0 {
			networks := swarmNetworks(m.Source.Spec)
			if len(networks) != 1 {
				return errors.New("Red original incompleta")
			}
			n, _ := networks[0].(map[string]any)
			if stringValue(n, "Target") != network.ID {
				return errors.New("El servicio original no usa la red compatible del proxy")
			}
			if len(array(n, "Aliases")) > 0 {
				return errors.New("Los alias personalizados de red requieren revisión para evitar colisiones")
			}
		} else {
			m.TargetNetwork = network.ID
		}
	}
	raw, e := a.run(ctx, p.target, `command -v curl >/dev/null && test -S /var/run/docker.sock && docker service ls --format '{{json .Name}}'`)
	if e != nil {
		return errors.New("No se pudo comprobar el administrador Swarm y curl en el destino")
	}
	for _, line := range strings.Split(raw, "\n") {
		var name string
		json.Unmarshal([]byte(line), &name)
		if name == p.Name {
			return errors.New("Ya existe un servicio con el mismo nombre en el destino")
		}
	}
	mounts := map[string]bool{}
	for _, v := range m.Mounts {
		mounts[v.Source] = true
	}
	for volume := range mounts {
		raw, e = a.run(ctx, p.server, "docker volume inspect "+shellQuote(volume))
		var volumes []struct {
			Driver, Scope string
			Options       map[string]string
		}
		if e != nil || json.Unmarshal([]byte(raw), &volumes) != nil || len(volumes) != 1 || volumes[0].Driver != "local" || volumes[0].Scope != "local" || len(volumes[0].Options) > 0 {
			return errors.New("Un volumen no es local o utiliza opciones externas")
		}
		raw, e = a.run(ctx, p.server, "docker ps -aq --no-trunc --filter "+shellQuote("volume="+volume))
		if e != nil {
			return errors.New("No se pudieron comprobar los usuarios del volumen")
		}
		for _, id := range strings.Fields(raw) {
			if id != p.container.ID {
				return errors.New("Un volumen también lo usa otro contenedor; necesita un traslado conjunto")
			}
		}
	}
	raw, e = a.run(ctx, p.server, swarmVolumesCommand)
	if e != nil {
		return errors.New("No se pudieron comprobar los volúmenes compartidos entre servicios")
	}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var entry struct {
			Name   string
			Mounts []struct{ Source string }
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			return errors.New("La lista de dependencias Swarm es inválida")
		}
		for _, mount := range entry.Mounts {
			if mounts[mount.Source] && entry.Name != p.Name {
				return errors.New("Un volumen pertenece también a otro servicio Swarm")
			}
		}
	}
	return nil
}
func (a *App) swarmRoute(ctx context.Context, s Server, name string) (int, string, error) {
	var adopted int
	if err := a.db.QueryRow("SELECT count(*) FROM native_swarm_apps WHERE server_id=? AND service_name=?", s.ID, name).Scan(&adopted); err != nil {
		return 0, "", errors.New("No se pudo leer la definición local de Nodo")
	}
	if adopted > 0 {
		service, err := a.inspectSwarm(ctx, s, name)
		if err != nil {
			return 0, "", err
		}
		labels := object(object(object(service.Spec, "TaskTemplate"), "ContainerSpec"), "Labels")
		if stringValue(labels, "lat.nodo.managed") == "true" {
			port := 0
			for key, value := range labels {
				if strings.HasSuffix(key, ".loadbalancer.server.port") && strings.HasPrefix(key, "traefik.http.services.") {
					textValue, ok := value.(string)
					if !ok {
						return 0, "", errors.New("Puerto de proxy inválido")
					}
					n, err := strconv.Atoi(textValue)
					if err != nil || n < 1 || n > 65535 || (port > 0 && port != n) {
						return 0, "", errors.New("Las rutas nativas tienen puertos distintos")
					}
					port = n
				}
			}
			if port > 0 {
				return port, specHash(labels), nil
			}
			return 0, "", errors.New("El servicio adoptado no tiene un puerto de proxy identificable")
		}
	}
	raw, e := a.run(ctx, s, "cat -- "+shellQuote("/etc/dokploy/traefik/dynamic/"+name+".yml"))
	if e != nil {
		return 0, "", errors.New("No se pudo importar la ruta pública original de Traefik")
	}
	var doc struct {
		HTTP struct {
			Routers map[string]struct {
				Rule, Service string
				Middlewares   []string
			}
			Services map[string]struct {
				LoadBalancer struct{ Servers []struct{ URL string } } `yaml:"loadBalancer"`
			}
		}
	}
	if yaml.Unmarshal([]byte(raw), &doc) != nil || len(doc.HTTP.Routers) == 0 {
		return 0, "", errors.New("La ruta original del proxy necesita revisión")
	}
	port := 0
	for _, router := range doc.HTTP.Routers {
		if !regexp.MustCompile("^Host\\(`[^`]+`\\)$").MatchString(router.Rule) {
			return 0, "", errors.New("La ruta original usa reglas avanzadas; requiere una adopción específica")
		}
		for _, middleware := range router.Middlewares {
			if middleware != "redirect-to-https" {
				return 0, "", errors.New("La ruta usa autenticación o middlewares que deben adoptarse antes del traslado")
			}
		}
		backend, exists := doc.HTTP.Services[strings.TrimSuffix(router.Service, "@file")]
		if !exists || len(backend.LoadBalancer.Servers) != 1 {
			return 0, "", errors.New("La ruta depende de varios servicios")
		}
		u, e := url.Parse(backend.LoadBalancer.Servers[0].URL)
		if e != nil || u.Hostname() != name || u.Scheme != "http" || u.Path != "" {
			return 0, "", errors.New("La ruta contiene un backend externo o especial")
		}
		value, e := strconv.Atoi(u.Port())
		if e != nil || value < 1 || value > 65535 || (port != 0 && port != value) {
			return 0, "", errors.New("No se pudo determinar un único puerto interno")
		}
		port = value
	}
	return port, hashBytes([]byte(raw)), nil
}
func sortedMountNames(m []swarmVolume) []string {
	names := map[string]bool{}
	for _, v := range m {
		names[v.Source] = true
	}
	out := []string{}
	for v := range names {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
