package panel

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const maxZipSize = 64 << 20
const maxExpandedSize = 256 << 20

var projectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)

type zipProject struct {
	files           []*zip.File
	root, compose   string
	generated       []byte
	services        []string
	details         []string
	size            uint64
	config          map[string]any
	dockerfile      bool
	runtime         []byte
	environmentFile []byte
	hasDotEnv       bool
}

func safeArchivePath(name string) bool {
	return name != "" && len(name) <= 240 && !strings.ContainsAny(name, "\\:\x00\r\n") && !strings.HasPrefix(name, "/") && path.Clean(name) == strings.TrimSuffix(name, "/") && name != "." && name != ".." && !strings.HasPrefix(name, "../")
}
func relativeProjectPath(s string) bool {
	return s == "." || (safeArchivePath(strings.TrimPrefix(s, "./")) && !strings.Contains(s, "$") && !strings.Contains(s, "://"))
}
func zipRead(f *zip.File) ([]byte, error) {
	r, e := f.Open()
	if e != nil {
		return nil, e
	}
	defer r.Close()
	b, e := io.ReadAll(io.LimitReader(r, 2<<20))
	if e != nil || uint64(len(b)) != f.UncompressedSize64 {
		return nil, errors.New("Archivo de configuración demasiado grande o dañado")
	}
	return b, nil
}
func inspectZip(data []byte, hostPort, containerPort int) (*zipProject, error) {
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		return nil, errors.New("El archivo no es un ZIP válido")
	}
	if len(z.File) == 0 || len(z.File) > 6000 {
		return nil, errors.New("El ZIP debe contener entre 1 y 6000 entradas")
	}
	p := &zipProject{}
	seen := map[string]bool{}
	candidates := []string{}
	dockerfiles := []string{}
	for _, f := range z.File {
		if !safeArchivePath(f.Name) || f.Mode()&os.ModeSymlink != 0 || (!f.Mode().IsRegular() && !f.FileInfo().IsDir()) {
			return nil, errors.New("El ZIP contiene rutas inseguras, enlaces o archivos especiales")
		}
		key := strings.ToLower(strings.TrimSuffix(f.Name, "/"))
		if seen[key] {
			return nil, errors.New("El ZIP contiene rutas duplicadas")
		}
		seen[key] = true
		if f.UncompressedSize64 > maxZipSize || p.size+f.UncompressedSize64 > maxExpandedSize {
			return nil, errors.New("El ZIP supera 256 MiB descomprimido o 64 MiB por archivo")
		}
		p.size += f.UncompressedSize64
		if f.FileInfo().IsDir() {
			continue
		}
		p.files = append(p.files, f)
		switch path.Base(f.Name) {
		case "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml":
			candidates = append(candidates, f.Name)
		case "Dockerfile":
			dockerfiles = append(dockerfiles, f.Name)
		}
	}
	// Prefer the repository root, otherwise accept one enclosing folder. Never
	// select an arbitrary nested example project from a repository.
	selectRoot := func(names []string) string {
		for _, n := range names {
			if !strings.Contains(n, "/") {
				return n
			}
		}
		if len(names) == 1 && strings.Count(names[0], "/") == 1 {
			return names[0]
		}
		return ""
	}
	p.compose = selectRoot(candidates)
	var definition []byte
	if p.compose != "" {
		p.root = path.Dir(p.compose)
		count := 0
		for _, name := range candidates {
			if path.Dir(name) == p.root {
				count++
			}
		}
		if count != 1 {
			return nil, errors.New("Deja un solo archivo Compose principal en la carpeta raíz")
		}
		for _, f := range p.files {
			if f.Name == p.compose {
				definition, e = zipRead(f)
				if e != nil {
					return nil, e
				}
			}
		}
	} else {
		p.dockerfile = true
		dockerfile := selectRoot(dockerfiles)
		if dockerfile == "" {
			return nil, errors.New("Incluye compose.yaml o Dockerfile en la raíz del ZIP, o dentro de una única carpeta")
		}
		if hostPort < 1 || hostPort > 65535 || containerPort < 1 || containerPort > 65535 {
			return nil, errors.New("Para un Dockerfile indica el puerto del servidor y el puerto interno (1–65535)")
		}
		p.root = path.Dir(dockerfile)
		p.compose = path.Join(p.root, "nodo-compose.json")
		if seen[strings.ToLower(p.compose)] {
			return nil, errors.New("El nombre nodo-compose.json está reservado")
		}
		definition, _ = json.Marshal(map[string]any{"services": map[string]any{"app": map[string]any{"build": ".", "restart": "unless-stopped", "ports": []string{fmt.Sprintf("%d:%d", hostPort, containerPort)}}}})
		p.generated = definition
	}
	var config map[string]any
	if yaml.Unmarshal(definition, &config) != nil {
		return nil, errors.New("El archivo Compose no es válido")
	}
	if e = validateCompose(config); e != nil {
		return nil, e
	}
	p.config = config
	services, _ := config["services"].(map[string]any)
	p.services = sortedKeys(services)
	for _, name := range p.services {
		service := services[name].(map[string]any)
		if service["build"] != nil {
			p.details = append(p.details, name+": construir desde el proyecto")
		} else if image, ok := service["image"].(string); ok {
			p.details = append(p.details, name+": imagen "+image)
		}
		if ports, ok := service["ports"].([]any); ok && len(ports) > 0 {
			encoded, _ := json.Marshal(ports)
			p.details = append(p.details, name+": puertos "+string(encoded))
		}
	}
	for _, f := range p.files {
		if f.Name == path.Join(p.root, ".env") {
			p.hasDotEnv = true
		}
		if path.Base(f.Name) == ".env" {
			b, e := zipRead(f)
			if e != nil {
				return nil, e
			}
			for _, line := range strings.Split(string(b), "\n") {
				key, _, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export ")), "=")
				key = strings.TrimSpace(key)
				if strings.HasPrefix(key, "COMPOSE_") || strings.HasPrefix(key, "DOCKER_") {
					return nil, errors.New("El archivo .env no puede cambiar COMPOSE_* ni DOCKER_*")
				}
			}
		}
	}
	return p, nil
}
func validateCompose(c map[string]any) error {
	bad := func(reason string) error { return errors.New("Revisa el Compose: " + reason) }
	for _, key := range []string{"include", "extends"} {
		if c[key] != nil {
			return bad("incluye todos los servicios en un único archivo; no se admite " + key)
		}
	}
	services, ok := c["services"].(map[string]any)
	if !ok || len(services) == 0 || len(services) > 50 {
		return bad("debe definir de 1 a 50 servicios")
	}
	checkPath := func(value any) bool { s, ok := value.(string); return ok && relativeProjectPath(s) }
	for _, raw := range services {
		s, ok := raw.(map[string]any)
		if !ok {
			return bad("definición de servicio inválida")
		}
		for _, key := range []string{"extends", "container_name", "network_mode", "pid", "ipc", "uts", "userns_mode", "devices", "device_cgroup_rules", "volumes_from", "external_links", "cap_add", "post_start", "pre_stop", "develop", "provider", "profiles", "gpus", "use_api_socket"} {
			if s[key] != nil {
				return bad("la opción " + key + " necesita configuración manual")
			}
		}
		if v, ok := s["privileged"]; ok && v != false {
			return bad("no se permiten contenedores privilegiados")
		}
		if s["build"] == nil && s["image"] == nil {
			return bad("cada servicio necesita image o build")
		}
		if b := s["build"]; b != nil {
			if obj, ok := b.(map[string]any); ok {
				if obj["context"] != nil && !checkPath(obj["context"]) {
					return bad("build.context debe estar dentro del ZIP")
				}
				if obj["dockerfile"] != nil && !checkPath(obj["dockerfile"]) {
					return bad("Dockerfile debe estar dentro del ZIP")
				}
				for _, k := range []string{"additional_contexts", "ssh", "entitlements", "network", "secrets"} {
					if obj[k] != nil {
						return bad("build." + k + " necesita configuración manual")
					}
				}
			} else if !checkPath(b) {
				return bad("build debe apuntar a una carpeta dentro del ZIP")
			}
		}
		if env := s["env_file"]; env != nil {
			list, ok := env.([]any)
			if !ok {
				list = []any{env}
			}
			for _, v := range list {
				if obj, ok := v.(map[string]any); ok {
					v = obj["path"]
				}
				if !checkPath(v) {
					return bad("env_file debe estar dentro del ZIP")
				}
			}
		}
		if volumes, ok := s["volumes"].([]any); ok {
			for _, v := range volumes {
				if text, ok := v.(string); ok {
					parts := strings.Split(text, ":")
					if len(parts) > 1 {
						src := parts[0]
						if !relativeProjectPath(src) {
							return bad("los volúmenes deben ser propios del proyecto o carpetas relativas dentro del ZIP")
						}
					}
				} else if mount, ok := v.(map[string]any); ok {
					if mount["type"] != "volume" && mount["type"] != "bind" && mount["type"] != "tmpfs" {
						return bad("tipo de montaje no admitido")
					}
					if src := mount["source"]; src != nil && !checkPath(src) {
						return bad("el origen del montaje debe estar dentro del proyecto")
					}
				} else {
					return bad("montaje inválido")
				}
			}
		}
		if networks, ok := s["networks"].(map[string]any); ok {
			for _, raw := range networks {
				if n, ok := raw.(map[string]any); ok && (n["ipv4_address"] != nil || n["ipv6_address"] != nil) {
					return bad("no se admiten direcciones de red fijas")
				}
			}
		}
	}
	for _, kind := range []string{"volumes", "networks", "secrets", "configs"} {
		if entries, ok := c[kind].(map[string]any); ok {
			for _, raw := range entries {
				if v, ok := raw.(map[string]any); ok {
					for _, key := range []string{"external", "name", "driver_opts", "ipam"} {
						if v[key] != nil && v[key] != false {
							return bad(kind + " no puede reutilizar recursos externos ni nombres globales")
						}
					}
					if driver, ok := v["driver"].(string); ok && driver != "local" && driver != "bridge" {
						return bad("controlador externo no admitido")
					}
					if f := v["file"]; f != nil && !checkPath(f) {
						return bad(kind + " debe leer archivos dentro del ZIP")
					}
				}
			}
		}
	}
	return nil
}
func writeProjectTar(p *zipProject) (name string, result error) {
	f, e := os.CreateTemp("", "nodo-upload-*.tar")
	if e != nil {
		return "", e
	}
	name = f.Name()
	temporaryPath := name
	defer func() {
		f.Close()
		if result != nil {
			os.Remove(temporaryPath)
		}
	}()
	t := tar.NewWriter(f)
	for _, entry := range p.files {
		name := entry.Name
		if p.root != "." {
			if !strings.HasPrefix(name, p.root+"/") {
				continue
			}
			name = strings.TrimPrefix(name, p.root+"/")
		}
		// The private enclosing directory protects uploads on the host. Preserve
		// readable application files inside a Docker COPY for non-root images.
		mode := int64(0644)
		if entry.Mode()&0111 != 0 {
			mode = 0755
		}
		if e = t.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(entry.UncompressedSize64)}); e != nil {
			return "", e
		}
		r, e := entry.Open()
		if e != nil {
			return "", e
		}
		n, e := io.Copy(t, io.LimitReader(r, int64(entry.UncompressedSize64)+1))
		r.Close()
		if e != nil || n != int64(entry.UncompressedSize64) {
			return "", errors.New("El ZIP está dañado")
		}
	}
	if len(p.generated) > 0 {
		if e = t.WriteHeader(&tar.Header{Name: path.Base(p.compose), Mode: 0600, Size: int64(len(p.generated))}); e != nil {
			return "", e
		}
		if _, e = t.Write(p.generated); e != nil {
			return "", e
		}
	}
	if e = t.Close(); e != nil {
		return "", e
	}
	if e = f.Close(); e != nil {
		return "", e
	}
	return name, nil
}

// Inspect only the project structure. Existing secret values never leave the
// backend; the form receives service names and likely internal ports.
func (a *App) inspectDeployment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxZipSize+(1<<20))
	if r.ParseMultipartForm(8<<20) != nil {
		if r.MultipartForm != nil {
			r.MultipartForm.RemoveAll()
		}
		fail(w, 400, "Selecciona un ZIP de hasta 64 MiB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, _, e := r.FormFile("file")
	if e != nil {
		fail(w, 400, "Selecciona un ZIP")
		return
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, maxZipSize+1))
	if e != nil || len(data) > maxZipSize {
		fail(w, 400, "El ZIP supera 64 MiB")
		return
	}
	z, e := inspectZip(data, 8080, 80)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	out := []map[string]any{}
	services := z.config["services"].(map[string]any)
	for _, name := range z.services {
		service := services[name].(map[string]any)
		port := suggestedPort(service)
		if z.dockerfile {
			port = 0
			for _, f := range z.files {
				if f.Name == path.Join(z.root, "Dockerfile") {
					b, e := zipRead(f)
					if e == nil {
						match := regexp.MustCompile(`(?mi)^\s*EXPOSE\s+([0-9]{1,5})(?:/tcp)?(?:\s|$)`).FindStringSubmatch(string(b))
						if len(match) > 1 {
							port, _ = strconv.Atoi(match[1])
						}
					}
				}
			}
		}
		out = append(out, map[string]any{"name": name, "port": port})
	}
	reply(w, 200, map[string]any{"services": out, "dockerfile": z.dockerfile, "hasDotEnv": z.hasDotEnv})
}
func suggestedPort(service map[string]any) int {
	for _, key := range []string{"expose", "ports"} {
		if values, ok := service[key].([]any); ok {
			for _, v := range values {
				if m, ok := v.(map[string]any); ok {
					v = m["target"]
				}
				s := fmt.Sprint(v)
				s = strings.Split(s, "/")[0]
				parts := strings.Split(s, ":")
				n, e := strconv.Atoi(parts[len(parts)-1])
				if e == nil && n > 0 && n <= 65535 {
					return n
				}
			}
		}
	}
	return 0
}
func (a *App) deployPlan(w http.ResponseWriter, r *http.Request) {
	s, e := a.server(r)
	if e != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxZipSize+(1<<20))
	if e = r.ParseMultipartForm(8 << 20); e != nil {
		if r.MultipartForm != nil {
			r.MultipartForm.RemoveAll()
		}
		fail(w, 400, "Selecciona un ZIP de hasta 64 MiB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	name := strings.TrimSpace(r.FormValue("name"))
	if !projectPattern.MatchString(name) {
		fail(w, 400, "Usa un nombre de 2–40 letras minúsculas, números o guiones, empezando por una letra")
		return
	}
	f, _, e := r.FormFile("file")
	if e != nil {
		fail(w, 400, "Selecciona un ZIP")
		return
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, maxZipSize+1))
	if e != nil || len(data) > maxZipSize {
		fail(w, 400, "El ZIP supera 64 MiB")
		return
	}
	hostPort, _ := strconv.Atoi(r.FormValue("hostPort"))
	containerPort, _ := strconv.Atoi(r.FormValue("containerPort"))
	z, e := inspectZip(data, hostPort, containerPort)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	id, e := randomToken()
	if e != nil {
		fail(w, 500, "No se pudo preparar el despliegue")
		return
	}
	p := &operationPlan{ID: id, Kind: "deploy", Name: name, Source: s.Name, server: s, compose: path.Base(z.compose), project: name + "-" + id[:8], Summary: []string{fmt.Sprintf("%d servicio(s) · %.1f MiB descomprimido", len(z.services), float64(z.size)/(1<<20)), "Se creará un proyecto nuevo con sus propios recursos."}, Warnings: []string{"El código del ZIP se ejecutará en el servidor. Usa un proyecto de confianza."}, Blockers: []string{}}
	service := r.FormValue("service")
	if service == "" && len(z.services) == 1 {
		service = z.services[0]
	}
	env, e := validateEnvironment(r.FormValue("environment"))
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	if mode := r.FormValue("access"); mode != "" && mode != "port" {
		if z.config["services"].(map[string]any)[service] == nil {
			fail(w, 400, "Escoge el servicio público")
			return
		}
	}
	if e = a.prepareAccess(r.Context(), p, r.FormValue("access"), r.FormValue("domain"), service, containerPort, r.FormValue("https") == "true"); e != nil {
		fail(w, 400, e.Error())
		return
	}
	if e = configureDeployment(z, p, env, r.FormValue("envTarget")); e != nil {
		fail(w, 400, e.Error())
		return
	}
	if p.access == nil {
		p.Summary = append(p.Summary, z.details...)
		p.Warnings = append(p.Warnings, "Se utilizarán los puertos publicados por el proyecto, sujetos al firewall del servidor.")
	}
	archive, e := writeProjectTar(z)
	if e != nil {
		fail(w, 400, "No se pudo preparar el ZIP; comprueba su integridad")
		return
	}
	p.archive = archive
	p.runtime = z.runtime
	p.environment = z.environmentFile
	p.hasDotEnv = z.hasDotEnv
	if _, e = a.run(r.Context(), s, `docker info >/dev/null && docker compose version >/dev/null && command -v tar >/dev/null || exit 1; help=$(docker compose up --help) || exit 1; case "$help" in *--wait-timeout*) ;; *) exit 1;; esac`); e != nil {
		p.Blockers = append(p.Blockers, "El servidor necesita Docker, Docker Compose v2 y tar accesibles mediante SSH.")
	}
	a.savePlan(w, r, p)
}
func (a *App) runDeploy(ctx context.Context, p *operationPlan) error {
	if e := a.recheckAccess(ctx, p); e != nil {
		return e
	}
	f, e := os.Open(p.archive)
	if e != nil {
		return errorStep("leer el ZIP preparado; vuelve a subirlo")
	}
	defer f.Close()
	// Project identifiers are generated locally. The archive has only checked
	// relative regular files, never links, special files, or parent traversals.
	base := `"$HOME/.local/share/nodo/deployments/` + p.project + `"`
	a.operationState(p, "running", "Subiendo los archivos al servidor…")
	command := `set -eu; umask 077; mkdir -p "$HOME/.local/share/nodo/deployments"; mkdir ` + base + `; tar -xf - -C ` + base
	var upload io.Reader = f
	var reader *io.PipeReader
	var packed chan error
	if len(p.runtime) > 0 {
		var writer *io.PipeWriter
		reader, writer = io.Pipe()
		packed = make(chan error, 1)
		upload = reader
		go func() { err := packDeployment(f, writer, p); writer.CloseWithError(err); packed <- err }()
	}
	e = a.stream(ctx, p.server, command, upload, io.Discard)
	if reader != nil {
		reader.CloseWithError(e)
		packErr := <-packed
		if e == nil {
			e = packErr
		}
	}
	if e != nil {
		return errorStep("subir los archivos; puede quedar una carpeta de preparación en el destino")
	}
	a.operationState(p, "running", "Validando, construyendo e iniciando los servicios…")
	compose := "docker compose --project-name " + shellQuote(p.project) + " --project-directory . -f " + shellQuote(p.compose)
	work := base
	log := "nodo-deploy.log"
	if len(p.runtime) > 0 {
		work = base + "/source"
		log = "../nodo-deploy.log"
		compose = "docker compose --project-name " + shellQuote(p.project) + " --project-directory ."
		if p.hasDotEnv {
			compose += " --env-file .env"
		}
		compose += " --env-file ../nodo-runtime/variables.env -f ../nodo-runtime/compose.json"
	}
	prepare := ""
	if p.TemplateID == "supabase" {
		// The enclosing deployment directory stays private. These directories
		// are bind-mounted into containers that may run as non-root users.
		prepare = "chmod 0755 volumes volumes/functions volumes/functions/main volumes/functions/hello; "
	}
	waitSeconds := 120
	if p.deployWaitSeconds > waitSeconds && p.deployWaitSeconds <= 600 {
		waitSeconds = p.deployWaitSeconds
	}
	command = "set -eu; umask 077; cd " + work + "; " + prepare + compose + " config --quiet >" + log + " 2>&1; " + compose + " up -d --build --no-recreate --wait --wait-timeout " + strconv.Itoa(waitSeconds) + " >>" + log + " 2>&1"
	if e = a.stream(ctx, p.server, command, nil, io.Discard); e != nil {
		return errors.New("El despliegue no terminó correctamente; puede haber servicios iniciados. Revisa Aplicaciones y el registro ~/.local/share/nodo/deployments/" + p.project + "/nodo-deploy.log antes de reintentar")
	}
	return nil
}

func packDeployment(source io.Reader, destination io.Writer, p *operationPlan) error {
	input := tar.NewReader(source)
	output := tar.NewWriter(destination)
	for {
		header, e := input.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if !safeArchivePath(header.Name) || header.Typeflag != tar.TypeReg {
			return errors.New("Archivo preparado inválido")
		}
		header.Name = "source/" + header.Name
		if e = output.WriteHeader(header); e != nil {
			return e
		}
		if _, e = io.Copy(output, input); e != nil {
			return e
		}
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{"nodo-runtime/compose.json", p.runtime}, {"nodo-runtime/variables.env", p.environment}} {
		if e := output.WriteHeader(&tar.Header{Name: file.name, Mode: 0600, Size: int64(len(file.data))}); e != nil {
			return e
		}
		if _, e := output.Write(file.data); e != nil {
			return e
		}
	}
	return output.Close()
}
