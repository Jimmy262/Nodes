package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
)

type containerMount struct {
	Type, Name, Source, Destination, Driver, Mode, Propagation string
	RW                                                         bool
}
type containerSpec struct {
	ID         string `json:"Id"`
	Name       string
	Image      string
	Config     map[string]any
	HostConfig map[string]any
	Mounts     []containerMount
	State      struct {
		Running, Paused bool
		Status          string
		Health          struct{ Status string }
	}
	NetworkSettings struct{ Networks map[string]json.RawMessage }
}

func (a *App) inspectContainer(ctx context.Context, s Server, id string) (*containerSpec, error) {
	if len(id) != 64 || !containerPattern.MatchString(id) {
		return nil, errors.New("ID inválido")
	}
	out, e := a.run(ctx, s, "docker inspect --type container "+id)
	if e != nil {
		return nil, e
	}
	var list []containerSpec
	if json.Unmarshal([]byte(out), &list) != nil || len(list) != 1 || list[0].ID != id || list[0].Config == nil || list[0].HostConfig == nil {
		return nil, errors.New("Inspección inválida")
	}
	return &list[0], nil
}
func (c *containerSpec) labels() map[string]any {
	v, _ := c.Config["Labels"].(map[string]any)
	return v
}
func (c *containerSpec) manager() string {
	l := c.labels()
	if l["com.docker.swarm.service.name"] != nil {
		return "Swarm"
	}
	if l["com.docker.compose.project"] != nil {
		return "Compose"
	}
	return "Docker independiente"
}
func (c *containerSpec) protected() bool {
	n := strings.ToLower(strings.TrimPrefix(c.Name, "/"))
	return strings.HasPrefix(n, "dokploy") || strings.HasPrefix(n, "traefik")
}
func (c *containerSpec) fingerprint() string {
	b, _ := json.Marshal([]any{c.ID, c.Name, c.Config, c.HostConfig, c.Mounts, c.NetworkSettings.Networks})
	return tokenHash(string(b))
}
func (a *App) daemon(ctx context.Context, s Server) (map[string]string, error) {
	out, e := a.run(ctx, s, `docker info --format '{"id":{{json .ID}},"arch":{{json .Architecture}},"os":{{json .OSType}}}'`)
	var d map[string]string
	if e != nil || json.Unmarshal([]byte(out), &d) != nil || d["id"] == "" {
		return nil, errors.New("No se pudo comprobar Docker")
	}
	return d, nil
}
func migrationBlockers(c *containerSpec) []string {
	b := []string{}
	h := c.HostConfig
	if c.manager() != "Docker independiente" {
		b = append(b, "Esta aplicación está administrada por "+c.manager()+". Traslada el proyecto completo desde Dokploy/Compose, con su definición, secretos y volúmenes; copiar una sola tarea no conservaría sus dependencias.")
		return b
	}
	if c.State.Paused {
		b = append(b, "Reanuda o detén el contenedor antes de trasladarlo.")
	}
	for _, m := range c.Mounts {
		if m.Type != "volume" || m.Driver != "local" || m.Name == "" {
			b = append(b, "Tiene carpetas del host, montajes temporales o volúmenes externos. Requieren una copia y revisión específicas.")
			break
		}
	}
	mode, _ := h["NetworkMode"].(string)
	for _, m := range c.Mounts {
		if m.Mode != "" && m.Mode != "ro" && m.Mode != "rw" {
			b = append(b, "Un volumen tiene opciones de montaje especiales que requieren un traslado manual.")
		}
	}
	if mounts, ok := h["Mounts"].([]any); ok {
		for _, raw := range mounts {
			if mount, ok := raw.(map[string]any); ok {
				if options, ok := mount["VolumeOptions"].(map[string]any); ok && options["Subpath"] != nil && options["Subpath"] != "" {
					b = append(b, "Un volumen usa un subdirectorio específico; requiere un traslado manual.")
				}
			}
		}
	}
	if mode != "default" && mode != "bridge" {
		b = append(b, "Usa una red personalizada o compartida. Debe trasladarse junto con la configuración de esa red.")
	}
	for name, raw := range c.NetworkSettings.Networks {
		if name != "bridge" {
			b = append(b, "La red "+name+" debe configurarse en el destino.")
			continue
		}
		var n map[string]any
		json.Unmarshal(raw, &n)
		if n["IPAMConfig"] != nil {
			b = append(b, "Tiene direcciones IP asignadas manualmente.")
		}
	}
	for _, key := range []string{"Privileged", "AutoRemove", "PublishAllPorts"} {
		if v, _ := h[key].(bool); v {
			b = append(b, "La opción "+key+" requiere un traslado manual.")
		}
	}
	for _, key := range []string{"Devices", "DeviceRequests", "VolumesFrom", "Links", "Tmpfs"} {
		if v := h[key]; v != nil {
			encoded, _ := json.Marshal(v)
			if string(encoded) != "[]" && string(encoded) != "{}" {
				b = append(b, "La opción "+key+" depende del host o de otros contenedores.")
			}
		}
	}
	for _, key := range []string{"PidMode", "IpcMode", "UTSMode", "UsernsMode", "CgroupParent", "CgroupnsMode", "Runtime"} {
		v, _ := h[key].(string)
		if v != "" && v != "private" && v != "runc" {
			b = append(b, "La opción "+key+" necesita revisión en el destino.")
		}
	}
	for key := range c.labels() {
		if strings.HasPrefix(key, "traefik.") || strings.HasPrefix(key, "dokploy") || strings.HasPrefix(key, "caddy") {
			b = append(b, "Tiene configuración de un proxy externo. Traslada la aplicación desde su administrador para conservar las rutas públicas.")
			break
		}
	}
	bindings, _ := h["PortBindings"].(map[string]any)
	for _, raw := range bindings {
		list, _ := raw.([]any)
		for _, item := range list {
			binding, _ := item.(map[string]any)
			ip, _ := binding["HostIp"].(string)
			port, _ := binding["HostPort"].(string)
			if (ip != "" && ip != "0.0.0.0" && ip != "127.0.0.1" && ip != "::") || port == "" || port == "0" {
				b = append(b, "Los puertos usan una IP del servidor o asignación automática. Define puertos portables antes del traslado.")
			}
		}
	}
	return b
}
func (a *App) planMigration(ctx context.Context, p *operationPlan) {
	c := p.container
	if c.manager() == "Swarm" && !c.protected() {
		a.planManagedMigration(ctx, p)
		return
	}
	p.Blockers = append(p.Blockers, migrationBlockers(c)...)
	p.Summary = []string{"Se detendrá el original durante todo el traslado y se conservará detenido al terminar.", "Se copiarán la capa de archivos, variables, comando, límites, puertos y volúmenes locales compatibles.", "Se creará un contenedor independiente con el mismo nombre en el destino."}
	for _, m := range c.Mounts {
		p.Summary = append(p.Summary, "Montaje: "+m.Type+" · "+m.Destination)
	}
	p.Warnings = append(p.Warnings, "Habrá una interrupción del servicio. DNS, certificados, proxy y bases de datos externas requieren revisión aparte.", "Si falla, se intentará detener la copia y reactivar el original. Si no se puede comprobar el destino, el original se dejará detenido para evitar dos copias activas.", "Al terminar se desactivará el reinicio automático del original; sus archivos y volúmenes se conservarán.")
	if len(p.Blockers) > 0 {
		p.Summary = []string{}
		return
	}
	if e := a.checkMigration(ctx, p); e != nil {
		p.Blockers = append(p.Blockers, e.Error())
	}
}
func (a *App) checkMigration(ctx context.Context, p *operationPlan) error {
	src, e := a.daemon(ctx, p.server)
	if e != nil {
		return errors.New("No se pudo comprobar Docker en el origen")
	}
	dst, e := a.daemon(ctx, p.target)
	if e != nil {
		return errors.New("No se pudo comprobar Docker en el destino")
	}
	if src["id"] == dst["id"] {
		return errors.New("Los dos registros apuntan al mismo motor Docker")
	}
	if src["arch"] != dst["arch"] || src["os"] != "linux" || dst["os"] != "linux" {
		return errors.New("El origen y el destino deben usar Linux y la misma arquitectura")
	}
	out, e := a.run(ctx, p.target, `command -v curl >/dev/null && test -S /var/run/docker.sock && docker ps -a --format '{{json .Names}}'`)
	if e != nil {
		return errors.New("El destino necesita curl y acceso al socket local de Docker")
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var name string
		json.Unmarshal([]byte(line), &name)
		if name == p.Name {
			return errors.New("Ya existe un contenedor con ese nombre en el destino")
		}
	}
	for _, m := range p.container.Mounts {
		out, e = a.run(ctx, p.server, "docker volume inspect "+shellQuote(m.Name))
		var volumes []struct {
			Driver, Scope string
			Options       map[string]string
		}
		if e != nil || json.Unmarshal([]byte(out), &volumes) != nil || len(volumes) != 1 || volumes[0].Driver != "local" || len(volumes[0].Options) != 0 || volumes[0].Scope != "local" {
			return errors.New("Un volumen usa opciones externas que no se pueden copiar automáticamente")
		}
		out, e = a.run(ctx, p.server, "docker ps -aq --no-trunc --filter "+shellQuote("volume="+m.Name))
		if e != nil {
			return errors.New("No se pudieron comprobar los usuarios del volumen")
		}
		for _, id := range strings.Fields(out) {
			if id != p.container.ID {
				return errors.New("Un volumen está compartido con otro contenedor. Deben trasladarse juntos para mantener la consistencia")
			}
		}
	}
	return nil
}

// Transfer directly over two SSH connections; no images or volume data are
// written to the Windows disk. A failing reader closes the other side as well.
func (a *App) transfer(ctx context.Context, src Server, from string, dst Server, to string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	done := make(chan error, 1)
	go func() {
		e := a.stream(ctx, src, from, nil, &transferLimit{w: w, left: 10 << 30})
		w.CloseWithError(e)
		done <- e
	}()
	e := a.stream(ctx, dst, to, r, io.Discard)
	r.CloseWithError(e)
	if e != nil {
		cancel()
	}
	sourceErr := <-done
	if e != nil {
		return e
	}
	return sourceErr
}

type transferLimit struct {
	w    io.Writer
	left int64
}

func (l *transferLimit) Write(b []byte) (int, error) {
	if int64(len(b)) > l.left {
		return 0, errors.New("La transferencia supera 10 GiB")
	}
	n, e := l.w.Write(b)
	l.left -= int64(n)
	return n, e
}
func restartPolicy(c *containerSpec) string {
	p, _ := c.HostConfig["RestartPolicy"].(map[string]any)
	name, _ := p["Name"].(string)
	if name == "" {
		return "no"
	}
	if name == "on-failure" {
		if n, _ := p["MaximumRetryCount"].(float64); n > 0 {
			b, _ := json.Marshal(n)
			return name + ":" + string(b)
		}
	}
	return name
}

const volumeHelper = "alpine:3.21"

func volumeCommand(volume string, write bool) string {
	mode, command := "readonly,", "tar -C /data -cpf - ."
	if write {
		mode = ""
		command = "tar -C /data -xpf -"
	}
	return "docker run --rm -i --network none --read-only --mount " + shellQuote("type=volume,"+mode+"src="+volume+",dst=/data") + " " + volumeHelper + " " + command
}
func (a *App) runMigration(ctx context.Context, p *operationPlan) (result error) {
	if !a.sameConnection(p.target) {
		return errors.New("La conexión de destino cambió o se retiró. Prepara una nueva revisión")
	}
	if e := a.checkMigration(ctx, p); e != nil {
		return e
	}
	c := p.container
	tag := "nodo-transfer:" + p.ID[:16]
	if len(c.Mounts) > 0 {
		a.operationState(p, "running", "Preparando la herramienta de copia en ambos servidores…")
		for _, s := range []Server{p.server, p.target} {
			if e := a.stream(ctx, s, "docker image inspect "+volumeHelper+" >/dev/null 2>&1 || docker pull "+volumeHelper+" >/dev/null", nil, io.Discard); e != nil {
				return errorStep("preparar la copia de volúmenes; el original no se detuvo")
			}
		}
	}
	stopped, created := false, false
	defer func() {
		if result == nil || !stopped {
			return
		}
		rollback, done := context.WithTimeout(context.Background(), 2*time.Minute)
		defer done()
		if created {
			if e := a.stream(rollback, p.target, "test \"$(docker inspect --format '{{index .Config.Labels \"lat.nodo.transfer\"}}' "+shellQuote(p.Name)+")\" = "+shellQuote(p.ID)+" && docker update --restart=no "+shellQuote(p.Name)+" >/dev/null && docker stop --time 30 "+shellQuote(p.Name)+" >/dev/null", nil, io.Discard); e != nil {
				result = errors.New("El traslado falló y no se pudo confirmar que la copia esté detenida. Ambos servidores requieren revisión; el original se conserva detenido.")
				return
			}
		}
		command := "docker update --restart=" + shellQuote(restartPolicy(c)) + " " + c.ID + " >/dev/null"
		if c.State.Running {
			command += " && docker start " + c.ID + " >/dev/null"
		}
		if e := a.stream(rollback, p.server, command, nil, io.Discard); e != nil {
			result = errors.New("El traslado falló y no se pudo reactivar el original. Se conservaron los datos; revisa ambos servidores.")
		} else {
			result = errors.New("El traslado no se completó. Se restauró el estado del original; pueden quedar una copia detenida, imágenes y volúmenes de recuperación en el destino.")
		}
	}()
	a.operationState(p, "running", "Deteniendo el original y guardando su estado…")
	// Mark before issuing the command: SSH can disconnect after the daemon acted.
	stopped = true
	if e := a.stream(ctx, p.server, "docker update --restart=no "+c.ID+" >/dev/null && docker stop --time 30 "+c.ID+" >/dev/null", nil, io.Discard); e != nil {
		return errorStep("detener el original")
	}
	if e := a.stream(ctx, p.server, "docker commit "+c.ID+" "+tag+" >/dev/null", nil, io.Discard); e != nil {
		return errorStep("guardar la capa de archivos")
	}
	a.operationState(p, "running", "Transfiriendo imagen y archivos del contenedor…")
	if e := a.transfer(ctx, p.server, "docker image save "+tag, p.target, "docker image load >/dev/null"); e != nil {
		return errorStep("transferir la imagen (máximo 10 GiB)")
	}
	mounts := []map[string]any{}
	names := map[string]string{}
	for _, m := range c.Mounts {
		name, exists := names[m.Name]
		if !exists {
			name = "nodo-" + p.ID[:16] + "-v" + strings.Repeat("x", len(names)+1)
			names[m.Name] = name
			a.operationState(p, "running", "Copiando los datos de los volúmenes…")
			if e := a.stream(ctx, p.target, "docker volume create "+name+" >/dev/null", nil, io.Discard); e != nil {
				return errorStep("crear el volumen de destino")
			}
			if e := a.transfer(ctx, p.server, volumeCommand(m.Name, false), p.target, volumeCommand(name, true)); e != nil {
				return errorStep("copiar el volumen (máximo 10 GiB por volumen)")
			}
		}
		mounts = append(mounts, map[string]any{"Type": "volume", "Source": name, "Target": m.Destination, "ReadOnly": !m.RW, "VolumeOptions": map[string]any{"NoCopy": true}})
	}
	config := jsonMap(c.Config)
	host := jsonMap(c.HostConfig)
	config["Image"] = tag
	labels, _ := config["Labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	labels["lat.nodo.transfer"] = p.ID
	config["Labels"] = labels
	host["Binds"] = nil
	host["Mounts"] = mounts
	host["VolumesFrom"] = nil
	host["NetworkMode"] = "bridge"
	config["HostConfig"] = host
	body, e := json.Marshal(config)
	if e != nil {
		return errorStep("preparar la configuración")
	}
	a.operationState(p, "running", "Creando y comprobando la copia en el destino…")
	// No shell interpolation of environment variables or container settings.
	command := "curl --silent --fail --unix-socket /var/run/docker.sock -H 'Content-Type: application/json' --data-binary @- " + shellQuote("http://localhost/containers/create?name="+url.QueryEscape(p.Name)) + " >/dev/null"
	created = true
	if e = a.stream(ctx, p.target, command, strings.NewReader(string(body)), io.Discard); e != nil {
		return errorStep("crear el contenedor de destino")
	}
	if e = a.stream(ctx, p.target, "docker start "+shellQuote(p.Name)+" >/dev/null", nil, io.Discard); e != nil {
		return errorStep("iniciar la copia")
	}
	for tries := 0; tries < 30; tries++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		out, e := a.run(ctx, p.target, "docker inspect --format '{{json .State}}' "+shellQuote(p.Name))
		var state struct {
			Running bool
			Health  *struct{ Status string }
		}
		if e != nil || json.Unmarshal([]byte(out), &state) != nil || !state.Running {
			return errorStep("comprobar la copia")
		}
		if state.Health == nil || state.Health.Status == "healthy" {
			return nil
		}
		if state.Health.Status == "unhealthy" {
			return errorStep("comprobación de salud")
		}
	}
	return errorStep("esperar la salud de la copia")
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
