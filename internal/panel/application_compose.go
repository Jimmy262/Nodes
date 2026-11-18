package panel

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

type editableCompose struct {
	File, WorkingDir, Project string
	PrivateRoot               string
	EnvFiles                  []string
	Source                    []byte
	Resolved                  map[string]any
}

func serverFile(value string) bool {
	return strings.HasPrefix(value, "/") && len(value) < 2048 && path.Clean(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}
func composeEditorCommand(c *editableCompose, file string) string {
	command := "docker compose --project-name " + shellQuote(c.Project) + " --project-directory " + shellQuote(c.WorkingDir)
	for _, f := range c.EnvFiles {
		command += " --env-file " + shellQuote(f)
	}
	return command + " -f " + shellQuote(file)
}
func composeSource(raw []byte) (map[string]any, error) {
	var node yaml.Node
	if yaml.Unmarshal(raw, &node) != nil {
		return nil, errors.New("No se pudo leer el archivo Compose")
	}
	var visit func(*yaml.Node) bool
	visit = func(n *yaml.Node) bool {
		if n.Tag != "" && !strings.HasPrefix(n.Tag, "!!") {
			return false
		}
		for _, child := range n.Content {
			if !visit(child) {
				return false
			}
		}
		return true
	}
	if !visit(&node) {
		return nil, errors.New("Este Compose usa etiquetas YAML especiales; necesita su editor original")
	}
	var doc map[string]any
	if node.Decode(&doc) != nil || doc["include"] != nil {
		return nil, errors.New("El proyecto incluye otros archivos; edítalo desde su administrador")
	}
	services, ok := doc["services"].(map[string]any)
	if !ok || len(services) == 0 {
		return nil, errors.New("El archivo no contiene servicios Compose")
	}
	for _, v := range services {
		s, ok := v.(map[string]any)
		if !ok || s["extends"] != nil {
			return nil, errors.New("Los servicios heredados requieren su editor original")
		}
	}
	return doc, nil
}
func (a *App) loadComposeConfiguration(ctx context.Context, s Server, items []*inspectedContainer, c *applicationConfiguration) error {
	l := items[0].Labels
	project := &editableCompose{File: l["com.docker.compose.project.config_files"], WorkingDir: l["com.docker.compose.project.working_dir"], Project: c.appName}
	if strings.Contains(project.File, ",") || !serverFile(project.File) || !serverFile(project.WorkingDir) {
		return errors.New("Para editar este proyecto, Nodo necesita un único archivo Compose y su carpeta original.")
	}
	for _, item := range items {
		if item.Labels["com.docker.compose.project.config_files"] != project.File || item.Labels["com.docker.compose.project.working_dir"] != project.WorkingDir {
			return errors.New("Los servicios usan configuraciones diferentes. Revisa el proyecto antes de editarlo.")
		}
	}
	if files := l["com.docker.compose.project.environment_file"]; files != "" {
		for _, f := range strings.Split(files, ",") {
			if !serverFile(f) {
				return errors.New("No se pudo identificar el archivo de variables del proyecto")
			}
			project.EnvFiles = append(project.EnvFiles, f)
		}
	}
	command := "test -f " + shellQuote(project.File) + " && test ! -L " + shellQuote(project.File) + " && test $(wc -c < " + shellQuote(project.File) + ") -lt 524288 && cat -- " + shellQuote(project.File)
	raw, err := a.run(ctx, s, command)
	if err != nil {
		return errors.New("El archivo Compose original no está disponible o es un enlace simbólico. Puedes cambiar el nombre visible.")
	}
	project.Source = []byte(raw)
	remoteHome, homeErr := a.run(ctx, s, `printf '%s' "$HOME"`)
	if homeErr != nil || !serverFile(remoteHome) {
		return errors.New("No se pudo preparar una ubicación privada para las variables")
	}
	project.PrivateRoot = path.Join(remoteHome, ".local/share/nodo/app-config")
	if _, err = composeSource(project.Source); err != nil {
		return err
	}
	// config's JSON output escapes literal dollars for subsequent Compose use.
	out, err := a.run(ctx, s, "cd "+shellQuote(project.WorkingDir)+" && "+composeEditorCommand(project, project.File)+" config --format json 2>/dev/null")
	if err != nil || len(out) >= 1<<20 || json.Unmarshal([]byte(out), &project.Resolved) != nil {
		return errors.New("Compose no pudo resolver el proyecto. Revisa sus archivos de variables antes de editarlo.")
	}
	services, ok := project.Resolved["services"].(map[string]any)
	if !ok {
		return errors.New("La configuración Compose está incompleta")
	}
	for name, raw := range services {
		service, _ := raw.(map[string]any)
		vars := []envValue{}
		if environment, ok := service["environment"].(map[string]any); ok {
			for key, value := range environment {
				if value != nil {
					vars = append(vars, envValue{Name: key, Value: strings.ReplaceAll(fmt.Sprint(value), "$$", "$")})
				}
			}
		}
		sort.Slice(vars, func(i, j int) bool { return vars[i].Name < vars[j].Name })
		c.Environment = append(c.Environment, configurationService{Name: name, Variables: vars})
		labels, err := composeStringMap(service["labels"])
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for key, rule := range labels {
			if !strings.HasPrefix(key, "traefik.http.routers.") || !strings.HasSuffix(key, ".rule") {
				continue
			}
			links := ruleLinks(rule, true, "")
			if len(links) != 1 {
				continue
			}
			host := strings.TrimPrefix(links[0].URL, "https://")
			if !validDomain(host) {
				continue
			}
			base := strings.TrimSuffix(key, ".rule")
			tls := labels[base+".tls"] == "true" || labels[base+".tls.certresolver"] != ""
			portKey := "traefik.http.services." + labels[base+".service"] + ".loadbalancer.server.port"
			port, _ := strconv.Atoi(labels[portKey])
			if port == 0 {
				for k, v := range labels {
					if strings.HasSuffix(k, ".loadbalancer.server.port") {
						port, _ = strconv.Atoi(v)
						break
					}
				}
			}
			if port == 0 {
				port = 80
			}
			id := name + "|" + host
			if seen[id] {
				if tls {
					for i := range c.Domains {
						if c.Domains[i].ID == id {
							c.Domains[i].HTTPS = true
						}
					}
				}
				continue
			}
			seen[id] = true
			c.Domains = append(c.Domains, editableDomain{ID: id, Host: host, Service: name, Port: port, HTTPS: tls, Path: "/"})
		}
	}
	sort.Slice(c.Environment, func(i, j int) bool { return c.Environment[i].Name < c.Environment[j].Name })
	sort.Slice(c.Domains, func(i, j int) bool { return c.Domains[i].ID < c.Domains[j].ID })
	c.compose = project
	c.Message = "Los valores se guardan en el Compose original. Solo se recrean los servicios que cambies. El nombre es el que verás en Nodo."
	return nil
}
func composeStringMap(raw any) (map[string]string, error) {
	out := map[string]string{}
	switch values := raw.(type) {
	case nil:
	case map[string]any:
		for k, v := range values {
			if v == nil {
				out[k] = ""
			} else {
				out[k] = fmt.Sprint(v)
			}
		}
	case []any:
		for _, v := range values {
			s, ok := v.(string)
			if !ok {
				return nil, errors.New("Lista Compose inválida")
			}
			k, value, _ := strings.Cut(s, "=")
			out[k] = value
		}
	default:
		return nil, errors.New("Formato Compose no compatible")
	}
	return out, nil
}
func prepareComposeEdit(p *operationPlan) error {
	e := p.edit
	doc, err := composeSource(e.before.compose.Source)
	if err != nil {
		return err
	}
	services := doc["services"].(map[string]any)
	affected := map[string]bool{}
	e.variableFiles = map[string][]byte{}
	if e.envChanged {
		if resolved, ok := e.before.compose.Resolved["services"].(map[string]any); ok {
			for _, raw := range resolved {
				service, _ := raw.(map[string]any)
				build, _ := service["build"].(map[string]any)
				contextPath, _ := build["context"].(string)
				if strings.HasPrefix(contextPath, "/") && (e.before.compose.PrivateRoot == path.Clean(contextPath) || strings.HasPrefix(e.before.compose.PrivateRoot, strings.TrimSuffix(path.Clean(contextPath), "/")+"/")) {
					return errors.New("El contexto de construcción incluye la carpeta privada de Nodo. Reduce ese contexto antes de guardar variables")
				}
			}
		}
	}
	for index, change := range e.input.Environment {
		service, ok := services[change.Name].(map[string]any)
		if !ok {
			return errors.New("Servicio Compose desconocido")
		}
		values := map[string]any{}
		switch original := service["environment"].(type) {
		case map[string]any:
			values = original
		case []any:
			for _, raw := range original {
				k, v, has := strings.Cut(fmt.Sprint(raw), "=")
				if has {
					values[k] = v
				} else {
					values[k] = nil
				}
			}
		case nil:
		default:
			return errors.New("Formato de variables no compatible")
		}
		old := map[string]string{}
		for _, item := range e.before.Environment {
			if item.Name == change.Name {
				for _, v := range item.Variables {
					old[v.Name] = v.Value
				}
			}
		}
		updates := []envValue{}
		for _, v := range change.Variables {
			previous, exists := old[v.Name]
			if !exists || previous != v.Value {
				delete(values, v.Name)
				updates = append(updates, v)
				affected[change.Name] = true
			}
		}
		service["environment"] = values
		if len(updates) > 0 {
			file := fmt.Sprintf("variables-%d.env", index)
			e.variableFiles[file] = environmentFile(updates)
			files := []any{}
			switch previous := service["env_file"].(type) {
			case nil:
			case []any:
				files = previous
			default:
				files = append(files, previous)
			}
			files = append(files, path.Join(e.before.compose.PrivateRoot, p.ID, file))
			service["env_file"] = files
		}
	}
	if e.domainChanged {
		d := e.input.Domain
		service, ok := services[d.Service].(map[string]any)
		if !ok {
			return errors.New("Servicio público desconocido")
		}
		labels, err := composeStringMap(service["labels"])
		if err != nil {
			return err
		}
		if d.ID != "" {
			var old editableDomain
			for _, v := range e.before.Domains {
				if v.ID == d.ID {
					old = v
				}
			}
			var proxy proxyOptions
			if p.access != nil {
				proxy = p.access.Proxy
			}
			if err := editDomainLabels(labels, old, d, proxy); err != nil {
				return err
			}
		} else {
			if service["network_mode"] != nil {
				return errors.New("Este servicio utiliza un modo de red que necesita configuración manual")
			}
			proxy := p.access.Proxy
			if network := labels["traefik.docker.network"]; network != "" && network != proxy.Network {
				return errors.New("Este servicio usa otra red de Traefik. Conserva su configuración desde el administrador original")
			}
			router := "nodo-edit-" + p.ID[:12]
			base := "traefik.http.routers." + router
			labels["traefik.enable"] = "true"
			labels["traefik.docker.network"] = proxy.Network
			labels[base+".rule"] = "Host(`" + d.Host + "`)"
			labels[base+".service"] = router
			labels[base+".entrypoints"] = proxy.HTTPEntry
			labels["traefik.http.services."+router+".loadbalancer.server.port"] = strconv.Itoa(d.Port)
			if d.HTTPS {
				labels[base+".entrypoints"] = proxy.HTTPSEntry
				labels[base+".tls"] = "true"
				labels[base+".tls.certresolver"] = proxy.Resolver
				labels[base+"-http.rule"] = labels[base+".rule"]
				labels[base+"-http.entrypoints"] = proxy.HTTPEntry
				labels[base+"-http.service"] = router
				labels[base+"-http.middlewares"] = router + "-https"
				labels["traefik.http.middlewares."+router+"-https.redirectscheme.scheme"] = "https"
			}
			networks, ok := doc["networks"].(map[string]any)
			if !ok {
				networks = map[string]any{}
			}
			key := "nodo_proxy_" + p.ID[:12]
			networks[key] = map[string]any{"external": true, "name": proxy.Network}
			connected := map[string]any{}
			switch original := service["networks"].(type) {
			case nil:
				connected["default"] = nil
				if networks["default"] == nil {
					networks["default"] = map[string]any{}
				}
			case []any:
				for _, n := range original {
					connected[fmt.Sprint(n)] = nil
				}
			case map[string]any:
				connected = original
			default:
				return errors.New("Formato de redes no compatible")
			}
			connected[key] = nil
			service["networks"] = connected
			doc["networks"] = networks
			p.Warnings = append(p.Warnings, "Los puertos publicados que ya tenía el proyecto se conservan.")
		}
		service["labels"] = labels
		affected[d.Service] = true
	}
	e.composeBytes, err = yaml.Marshal(doc)
	if err != nil {
		return err
	}
	for name := range affected {
		e.services = append(e.services, name)
	}
	sort.Strings(e.services)
	return nil
}

// Resolve the backend from the matching router; never change unrelated services.
func updateDomainPort(labels map[string]string, host string, port int) error {
	keys := map[string]bool{}
	for key, rule := range labels {
		if !strings.HasPrefix(key, "traefik.http.routers.") || !strings.HasSuffix(key, ".rule") {
			continue
		}
		if !strings.Contains(rule, "`"+host+"`") && !strings.Contains(rule, "\""+host+"\"") {
			continue
		}
		service := labels[strings.TrimSuffix(key, ".rule")+".service"]
		if strings.Contains(service, "@") {
			continue
		}
		if service != "" {
			keys["traefik.http.services."+service+".loadbalancer.server.port"] = true
			continue
		}
		candidates := []string{}
		for candidate := range labels {
			if strings.HasPrefix(candidate, "traefik.http.services.") && strings.HasSuffix(candidate, ".loadbalancer.server.port") {
				candidates = append(candidates, candidate)
			}
		}
		if len(candidates) != 1 {
			return errors.New("No se pudo identificar el servicio de Traefik para cambiar el puerto")
		}
		keys[candidates[0]] = true
	}
	if len(keys) == 0 {
		return errors.New("No se pudo identificar el servicio de Traefik para cambiar el puerto")
	}
	for key := range keys {
		labels[key] = strconv.Itoa(port)
	}
	return nil
}
func composeEditScript(p *operationPlan) string {
	c := p.edit.before.compose
	tmp := c.File + ".nodo-" + p.ID[:16]
	base := shellQuote(path.Join(c.PrivateRoot, p.ID))
	selected := ""
	for _, name := range p.edit.services {
		selected += " " + shellQuote(name)
	}
	up := composeEditorCommand(c, c.File) + " up -d --no-build --pull never --no-deps --wait --wait-timeout 120" + selected
	check := `test "$(sha256sum -- ` + shellQuote(c.File) + ` | cut -d ' ' -f 1)" = ` + shellQuote(hashBytes(c.Source))
	return "set -eu; umask 077; cd " + shellQuote(c.WorkingDir) + "; test -f " + shellQuote(c.File) + "; test ! -L " + shellQuote(c.File) + "; " + check + "; " +
		"mkdir -p " + shellQuote(c.PrivateRoot) + "; mkdir " + base + "; tar -xf - -C " + base + "; test ! -e " + shellQuote(tmp) + "; (set -C; cat " + base + "/compose.next > " + shellQuote(tmp) + "); " +
		"trap " + shellQuote("rm -f -- "+shellQuote(tmp)) + " EXIT; " + composeEditorCommand(c, tmp) + " config --quiet > " + base + "/apply.log 2>&1; " + check + "; " +
		"cp -p -- " + shellQuote(c.File) + " " + base + "/compose.before; chmod 600 " + base + "/compose.before; mv -- " + shellQuote(tmp) + " " + shellQuote(c.File) + "; " +
		"if " + up + " >> " + base + "/apply.log 2>&1; then printf APPLIED; else cp -- " + base + "/compose.before " + shellQuote(tmp) + "; chmod 600 " + shellQuote(tmp) + "; mv -- " + shellQuote(tmp) + " " + shellQuote(c.File) + "; if " + up + " >> " + base + "/apply.log 2>&1; then printf ROLLED_BACK; else printf RECOVERY_REQUIRED; fi; exit 1; fi"
}

// GitHub releases have a separate policy that is reused by the next push.
// Keep a corrected port there too, under the deployer's own lock.
func composeEditCommand(p *operationPlan) string {
	command := composeEditScript(p)
	c := p.edit.before.compose
	d := p.edit.input.Domain
	if !p.edit.domainChanged || d == nil {
		return command
	}
	base := path.Dir(path.Dir(path.Dir(c.File)))
	id := path.Base(base)
	if path.Base(c.File) != "compose.json" || path.Base(path.Dir(path.Dir(c.File))) != "releases" || len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" || base != path.Join(path.Dir(c.PrivateRoot), "github", id) {
		return command
	}
	// All routing labels come from the reviewed Compose, outside the build context.
	var doc map[string]any
	if yaml.Unmarshal(p.edit.composeBytes, &doc) != nil {
		return command
	}
	services, _ := doc["services"].(map[string]any)
	service, _ := services[d.Service].(map[string]any)
	labels, _ := composeStringMap(service["labels"])
	ports := map[string]string{}
	for key, value := range labels {
		if strings.HasPrefix(key, "traefik.") {
			ports[key] = value
		}
	}
	patch, _ := json.Marshal(ports)
	script := `import fcntl, json, os, pathlib, subprocess, sys
base = pathlib.Path(sys.argv[1])
with (base / 'deploy.lock').open('a') as lock:
 fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
 target = base / 'policy.json'
 policy = json.loads(target.read_text())
 if policy['service'] != sys.argv[2]: raise RuntimeError('Public service changed')
 labels = policy['labels'][sys.argv[2]]
 ports = json.loads(sys.argv[4])
 if not ports: raise RuntimeError('Missing routing policy')
 policy['labels'][sys.argv[2]] = ports
 policy['port'] = int(sys.argv[3])
 temporary = base / ('policy-edit-' + sys.argv[5] + '.tmp')
 try:
  with temporary.open('x') as output:
   os.chmod(temporary, 0o600)
   json.dump(policy, output)
  subprocess.run(['sh', '-c', sys.argv[6]], check=True)
  temporary.replace(target)
 finally:
  temporary.unlink(missing_ok=True)
`
	return "python3 -c " + shellQuote(script) + " " + shellQuote(base) + " " + shellQuote(d.Service) + " " + strconv.Itoa(d.Port) + " " + shellQuote(string(patch)) + " " + shellQuote(p.ID) + " " + shellQuote(command)
}
func (a *App) applyComposeEdit(ctx context.Context, p *operationPlan) error {
	a.operationState(p, "running", "Guardando Compose y actualizando los servicios modificados…")
	out := &cappedBuffer{limit: 100}
	var payload bytes.Buffer
	tw := tar.NewWriter(&payload)
	files := map[string][]byte{"compose.next": p.edit.composeBytes}
	for name, data := range p.edit.variableFiles {
		files[name] = data
	}
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	defer clear(payload.Bytes())
	err := a.stream(ctx, p.server, composeEditCommand(p), &payload, out)
	if err != nil {
		if string(out.data) == "ROLLED_BACK" {
			return errors.New("No se pudo aplicar el cambio. Se restauró la configuración anterior y Compose recuperó los servicios")
		}
		return errors.New("No se pudo verificar la actualización. Revisa la aplicación y la copia privada en ~/.local/share/nodo/app-config/" + p.ID + " antes de reintentar")
	}
	if string(out.data) != "APPLIED" {
		return errors.New("El servidor no confirmó el cambio. Revisa la aplicación antes de reintentar")
	}
	p.edit.result = "Compose comprobó los servicios actualizados. Se conservaron sus volúmenes."
	return nil
}
