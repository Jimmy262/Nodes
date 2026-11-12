package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (a *App) nativeSwarmRecord(s Server, p *operationPlan, spec map[string]any, stage string) error {
	raw, e := json.Marshal(map[string]any{"spec": spec, "url": p.URL, "sourceServerId": p.server.ID, "targetServerId": p.target.ID})
	if e != nil {
		return e
	}
	defer clear(raw)
	protected, encrypted, e := sealDokployKey(s, raw)
	if e != nil {
		return errors.New("Windows no pudo proteger la definición; no se puede continuar")
	}
	_, e = a.db.Exec("INSERT INTO native_swarm_apps VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(server_id,service_name) DO UPDATE SET operation_id=excluded.operation_id,connection_hash=excluded.connection_hash,protected_key=excluded.protected_key,definition=excluded.definition,stage=excluded.stage,updated_at=excluded.updated_at", s.ID, p.Name, p.ID, connectionHash(s), protected, encrypted, stage, time.Now().UTC().Format(time.RFC3339Nano))
	return e
}
func (a *App) swarmAPI(ctx context.Context, s Server, endpoint string, body map[string]any, out io.Writer) error {
	b, e := json.Marshal(body)
	if e != nil {
		return e
	}
	defer clear(b)
	command := "curl -q --silent --fail --max-time 90 --noproxy '*' --unix-socket /var/run/docker.sock -H 'Content-Type: application/json' --data-binary @- " + shellQuote("http://localhost/"+endpoint)
	return a.stream(ctx, s, command, bytes.NewReader(b), out)
}
func (a *App) updateSwarm(ctx context.Context, s Server, current swarmService, spec map[string]any) error {
	return a.swarmAPI(ctx, s, "services/"+url.PathEscape(current.ID)+"/update?version="+strconv.FormatUint(current.Version.Index, 10), spec, io.Discard)
}
func replicasSpec(original map[string]any, replicas int, operation string) map[string]any {
	spec := cloneObject(original)
	spec["Mode"] = map[string]any{"Replicated": map[string]any{"Replicas": replicas}}
	if operation != "" {
		labels := object(spec, "Labels")
		if labels == nil {
			labels = map[string]any{}
		}
		labels["lat.nodo.migration"] = operation
		spec["Labels"] = labels
	}
	return spec
}
func targetSwarmSpec(p *operationPlan, image string, volumes map[string]string) map[string]any {
	m := p.swarm
	spec := replicasSpec(m.Source.Spec, 0, p.ID)
	labels := object(spec, "Labels")
	labels["lat.nodo.managed"] = "true"
	spec["Labels"] = labels
	delete(spec, "Networks")
	task := object(spec, "TaskTemplate")
	task["Networks"] = []any{map[string]any{"Target": m.TargetNetwork}}
	cs := object(task, "ContainerSpec")
	cs["Image"] = image
	mounts := array(cs, "Mounts")
	for _, raw := range mounts {
		mount := raw.(map[string]any)
		mount["Source"] = volumes[stringValue(mount, "Source")]
		mount["VolumeOptions"] = map[string]any{"NoCopy": true}
	}
	// Docker provider observes Swarm tasks on this single-node destination.
	containerLabels := object(cs, "Labels")
	if containerLabels == nil {
		containerLabels = map[string]any{}
	}
	for key := range containerLabels {
		if strings.HasPrefix(key, "traefik.") || strings.HasPrefix(key, "com.docker.swarm.") {
			delete(containerLabels, key)
		}
	}
	router := "nodo-" + p.ID[:16]
	base := "traefik.http.routers." + router
	containerLabels["lat.nodo.managed"] = "true"
	containerLabels["lat.nodo.transfer"] = p.ID
	containerLabels["traefik.enable"] = "true"
	containerLabels["traefik.docker.network"] = m.Proxy.Network
	containerLabels[base+".rule"] = "Host(`" + m.Domain + "`)"
	containerLabels[base+".entrypoints"] = m.Proxy.HTTPSEntry
	containerLabels[base+".tls"] = "true"
	containerLabels[base+".tls.certresolver"] = m.Proxy.Resolver
	containerLabels[base+".service"] = router
	containerLabels["traefik.http.services."+router+".loadbalancer.server.port"] = strconv.Itoa(m.Port)
	containerLabels[base+"-http.rule"] = containerLabels[base+".rule"]
	containerLabels[base+"-http.entrypoints"] = m.Proxy.HTTPEntry
	containerLabels[base+"-http.service"] = router
	containerLabels[base+"-http.middlewares"] = router + "-https"
	containerLabels["traefik.http.middlewares."+router+"-https.redirectscheme.scheme"] = "https"
	cs["Labels"] = containerLabels
	return spec
}
func (a *App) waitSwarmStopped(ctx context.Context, s Server, id string) error {
	for i := 0; i < 45; i++ {
		raw, e := a.run(ctx, s, "docker ps -q --no-trunc --filter "+shellQuote("label=com.docker.swarm.service.id="+id))
		if e != nil {
			return errors.New("No se pudo comprobar la detención de las tareas")
		}
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		if e = swarmPause(ctx); e != nil {
			return e
		}
	}
	return errors.New("Swarm todavía tiene tareas activas; no se pueden copiar los datos")
}
func swarmPause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return nil
	}
}
func (a *App) waitSwarmHealthy(ctx context.Context, s Server, serviceID, operation string) error {
	for i := 0; i < 60; i++ {
		raw, e := a.run(ctx, s, "docker ps -q --no-trunc --filter "+shellQuote("label=com.docker.swarm.service.id="+serviceID))
		if e != nil {
			return errors.New("No se pudieron consultar las tareas de destino")
		}
		ids := strings.Fields(raw)
		if len(ids) > 1 {
			return errors.New("El destino tiene más tareas de las previstas")
		}
		if len(ids) == 1 {
			if !containerPattern.MatchString(ids[0]) {
				return errors.New("Identidad de tarea inválida")
			}
			raw, e = a.run(ctx, s, `docker inspect --format '{"Running":{{json .State.Running}},"Health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}""{{end}},"Operation":{{json (index .Config.Labels "lat.nodo.transfer")}}}' `+ids[0])
			var state struct {
				Running           bool
				Health, Operation string
			}
			if e != nil || json.Unmarshal([]byte(raw), &state) != nil || (operation != "" && state.Operation != operation) {
				return errors.New("La tarea de destino no coincide con este traslado")
			}
			if state.Running && (state.Health == "" || state.Health == "healthy") {
				return nil
			}
		}
		if e = swarmPause(ctx); e != nil {
			return e
		}
	}
	return errors.New("La tarea de destino no llegó a estar ejecutándose y saludable")
}
func (a *App) fenceSwarmDestination(ctx context.Context, p *operationPlan) error {
	raw, e := a.run(ctx, p.target, `docker service ls --format '{{json .Name}}'`)
	if e != nil {
		return errors.New("No se pudo comprobar si existe la copia")
	}
	exists := false
	for _, line := range strings.Split(raw, "\n") {
		var name string
		json.Unmarshal([]byte(line), &name)
		if name == p.Name {
			exists = true
		}
	}
	if !exists {
		return nil
	}
	current, e := a.inspectSwarm(ctx, p.target, p.Name)
	if e != nil {
		return e
	}
	if stringValue(object(current.Spec, "Labels"), "lat.nodo.migration") != p.ID {
		return errors.New("No se puede detener un servicio ajeno a esta operación")
	}
	if e = a.updateSwarm(ctx, p.target, current, replicasSpec(current.Spec, 0, p.ID)); e != nil {
		return e
	}
	return a.waitSwarmStopped(ctx, p.target, current.ID)
}
func (a *App) restoreSwarmSource(ctx context.Context, p *operationPlan) error {
	current, e := a.inspectSwarm(ctx, p.server, p.Name)
	if e != nil {
		return e
	}
	original := p.swarm.Source
	if current.ID != original.ID {
		return errors.New("La identidad del original cambió")
	}
	if specHash(current.Spec) == specHash(original.Spec) {
		return nil
	}
	if specHash(current.Spec) != specHash(replicasSpec(original.Spec, 0, p.ID)) {
		return errors.New("El original fue modificado fuera del traslado")
	}
	return a.updateSwarm(ctx, p.server, current, original.Spec)
}
func (a *App) runSwarmMigration(ctx context.Context, p *operationPlan) (result error) {
	if !a.sameConnection(p.target) {
		return errors.New("La conexión de destino cambió. Revisa de nuevo el traslado")
	}
	m := p.swarm
	current, e := a.inspectSwarm(ctx, p.server, p.Name)
	if e != nil || current.ID != m.Source.ID || current.Version.Index != m.Source.Version.Index || specHash(current.Spec) != specHash(m.Source.Spec) {
		return errors.New("El servicio cambió después de la revisión; no se ha detenido")
	}
	c, e := a.inspectContainer(ctx, p.server, p.container.ID)
	if e != nil || c.fingerprint() != p.fingerprint {
		return errors.New("La tarea original cambió. Vuelve a revisar el traslado")
	}
	if e = a.checkSwarmResources(ctx, p); e != nil {
		return e
	}
	_, routeHash, e := a.swarmRoute(ctx, p.server, p.Name)
	if e != nil || routeHash != m.SourceRouteHash {
		return errors.New("La ruta original del proxy cambió. Vuelve a revisar el traslado")
	}
	proxy := a.proxyOptions(ctx, p.target)
	if !proxy.Available || !proxy.HTTPS || proxy.PublicIP != m.Proxy.PublicIP || proxy.Network != m.Proxy.Network || proxy.Resolver != m.Proxy.Resolver || proxy.HTTPEntry != m.Proxy.HTTPEntry || proxy.HTTPSEntry != m.Proxy.HTTPSEntry {
		return errors.New("El proxy de destino cambió. Revisa de nuevo el traslado")
	}
	if !a.checkSwarmDNS(ctx, m.Domain, proxy.PublicIP).Ready {
		return errors.New("El enlace del destino no resuelve correctamente. No se detuvo el original")
	}
	used, e := a.domainInUse(ctx, p.target, m.Domain)
	if e != nil {
		return e
	}
	if used {
		return errors.New("El dominio de destino fue ocupado por otra aplicación")
	}
	a.operationState(p, "running", "Guardando la definición cifrada de Swarm en Nodo y preparando la copia…")
	if e = a.nativeSwarmRecord(p.server, p, m.Source.Spec, "prepared"); e != nil {
		return e
	}
	for _, s := range []Server{p.server, p.target} {
		if e = a.stream(ctx, s, "docker image inspect "+volumeHelper+" >/dev/null 2>&1 || docker pull "+volumeHelper+" >/dev/null", nil, io.Discard); e != nil {
			return errors.New("No se pudo preparar la herramienta de copia. El original sigue activo")
		}
	}
	image := "nodo-transfer:" + p.ID[:16]
	// Swarm removes task containers when scaling down. Capture the current layer
	// first; docker commit pauses it briefly. Persistent data is copied after stop.
	a.operationState(p, "running", "Capturando la imagen de la tarea actual antes de detener Swarm…")
	if e = a.stream(ctx, p.server, "docker commit "+p.container.ID+" "+image+" >/dev/null", nil, io.Discard); e != nil {
		return errors.New("No se pudo capturar la imagen. No se redujeron las réplicas")
	}
	stopped, createAttempted := false, false
	defer func() {
		if result == nil || !stopped {
			return
		}
		recovery, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		a.operationState(p, "running", "El traslado falló. Comprobando y deteniendo la copia antes de recuperar el original…")
		if createAttempted {
			if err := a.fenceSwarmDestination(recovery, p); err != nil {
				a.nativeSwarmRecord(p.server, p, m.Source.Spec, "recovery-required")
				result = errors.New("El traslado falló y no se pudo confirmar la detención del destino. El original se conserva con cero réplicas. Revisa ambos servidores antes de reactivarlo")
				return
			}
		}
		if err := a.restoreSwarmSource(recovery, p); err != nil {
			a.nativeSwarmRecord(p.server, p, m.Source.Spec, "recovery-required")
			result = errors.New("El traslado falló y no se pudo recuperar automáticamente el original. Su definición y volúmenes se conservaron; revisa ambos servidores")
			return
		}
		if err := a.waitSwarmHealthy(recovery, p.server, m.Source.ID, ""); err != nil {
			a.nativeSwarmRecord(p.server, p, m.Source.Spec, "recovery-required")
			result = errors.New("Se restauró la definición original, pero no se pudo confirmar la salud de su tarea. Revisa el origen antes de reintentar")
			return
		}
		a.nativeSwarmRecord(p.server, p, m.Source.Spec, "restored")
		result = errors.New("El traslado no se completó. Se confirmó la detención de la copia y se restauró la definición original con una réplica. Puede quedar material de recuperación en el destino")
	}()
	a.operationState(p, "running", "Deteniendo el servicio original: reduciendo a cero réplicas y esperando el cierre de sus tareas…")
	stopped = true
	if e = a.updateSwarm(ctx, p.server, current, replicasSpec(m.Source.Spec, 0, p.ID)); e != nil {
		return errors.New("Swarm no confirmó la reducción de réplicas")
	}
	if e = a.waitSwarmStopped(ctx, p.server, m.Source.ID); e != nil {
		return e
	}
	if e = a.nativeSwarmRecord(p.server, p, m.Source.Spec, "source-stopped"); e != nil {
		return e
	}
	a.operationState(p, "running", "Original detenido. Transfiriendo la imagen por SSH (máximo 10 GiB)…")
	if e = a.transfer(ctx, p.server, "docker image save "+image, p.target, "docker image load >/dev/null"); e != nil {
		return errors.New("No se pudo transferir la imagen")
	}
	volumes := map[string]string{}
	for i, source := range sortedMountNames(m.Mounts) {
		target := "nodo-" + p.ID[:16] + "-v" + strconv.Itoa(i+1)
		volumes[source] = target
		a.operationState(p, "running", "Copiando volumen "+source+" con el servicio original detenido…")
		command := `test -z "$(docker volume ls -q --filter ` + shellQuote("name=^"+target+"$") + `)" && docker volume create --label ` + shellQuote("lat.nodo.transfer="+p.ID) + " " + shellQuote(target) + " >/dev/null"
		if e = a.stream(ctx, p.target, command, nil, io.Discard); e != nil {
			return errors.New("No se pudo reservar un volumen nuevo en el destino")
		}
		if e = a.transfer(ctx, p.server, volumeCommand(source, false), p.target, volumeCommand(target, true)); e != nil {
			return errors.New("No se pudo copiar el volumen " + source)
		}
	}
	targetSpec := targetSwarmSpec(p, image, volumes)
	if e = a.nativeSwarmRecord(p.target, p, targetSpec, "target-prepared"); e != nil {
		return e
	}
	a.operationState(p, "running", "Creando el servicio Swarm de destino con cero réplicas…")
	buffer := &cappedBuffer{limit: 8192}
	createAttempted = true
	if e = a.swarmAPI(ctx, p.target, "services/create", targetSpec, buffer); e != nil {
		return errors.New("Swarm no confirmó la creación del servicio de destino")
	}
	var created struct{ ID string }
	if json.Unmarshal(buffer.data, &created) != nil || created.ID == "" {
		return errors.New("Swarm devolvió una identidad de destino inválida")
	}
	target, e := a.inspectSwarm(ctx, p.target, p.Name)
	if e != nil || target.ID != created.ID || stringValue(object(target.Spec, "Labels"), "lat.nodo.migration") != p.ID {
		return errors.New("No se pudo verificar la identidad del servicio creado")
	}
	a.operationState(p, "running", "Iniciando una réplica en el destino y comprobando su salud…")
	active := replicasSpec(target.Spec, 1, p.ID)
	if e = a.updateSwarm(ctx, p.target, target, active); e != nil {
		return errors.New("No se pudo iniciar la réplica del destino")
	}
	if e = a.waitSwarmHealthy(ctx, p.target, created.ID, p.ID); e != nil {
		return e
	}
	a.operationState(p, "running", "Comprobando la respuesta HTTP del servicio por su red interna…")
	command := "docker run --rm --network " + shellQuote(m.Proxy.Network) + " --read-only " + volumeHelper + " wget -q -T 15 -O /dev/null --header=" + shellQuote("Host: "+m.Domain) + " " + shellQuote(fmt.Sprintf("http://%s:%d/", p.Name, m.Port))
	if e = a.stream(ctx, p.target, command, nil, io.Discard); e != nil {
		return errors.New("La copia no superó la comprobación HTTP. Puede necesitar ajustar variables de URL u origen")
	}
	if e = a.nativeSwarmRecord(p.target, p, active, "active"); e != nil {
		return errors.New("La copia respondió, pero no se pudo guardar su definición activa en Nodo")
	}
	if e = a.nativeSwarmRecord(p.server, p, m.Source.Spec, "migrated-source-stopped"); e != nil {
		return e
	}
	return nil
}
