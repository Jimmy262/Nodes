package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const terminalRequirementsCommand = codexEnvironment + `;
for name in tmux codex npm node; do if command -v "$name" >/dev/null 2>&1; then printf '%s=1\n' "$name"; else printf '%s=0\n' "$name"; fi; done
if [ "$(id -u)" = 0 ]; then printf 'admin=root\n'; elif command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then printf 'admin=sudo\n'; else printf 'admin=none\n'; fi
manager=none; for name in apt-get dnf yum apk; do if command -v "$name" >/dev/null 2>&1; then manager=$name; break; fi; done; printf 'manager=%s\n' "$manager"`

type terminalRequirements struct {
	Tmux         bool                 `json:"tmux"`
	Codex        bool                 `json:"codex"`
	NPM          bool                 `json:"npm"`
	Node         bool                 `json:"node"`
	Admin        string               `json:"admin"`
	Manager      string               `json:"manager"`
	Dependencies []terminalDependency `json:"dependencies"`
}
type terminalDependency struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Installed  bool   `json:"installed"`
	CanInstall bool   `json:"canInstall"`
	Reason     string `json:"reason,omitempty"`
}
type terminalInstallPlan struct{ Package, Snapshot string }

func (a *App) terminalRequirements(ctx context.Context, s Server) (terminalRequirements, error) {
	out, err := a.run(ctx, s, `exec "${SHELL:-/bin/sh}" -lc `+shellQuote(terminalRequirementsCommand))
	if err != nil {
		return terminalRequirements{}, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			values[key] = value
		}
	}
	for _, name := range []string{"tmux", "codex", "npm", "node"} {
		if values[name] != "0" && values[name] != "1" {
			return terminalRequirements{}, errors.New("No se pudieron comprobar los requisitos del servidor")
		}
	}
	r := terminalRequirements{Tmux: values["tmux"] == "1", Codex: values["codex"] == "1", NPM: values["npm"] == "1", Node: values["node"] == "1", Admin: values["admin"], Manager: values["manager"]}
	if r.Admin != "root" && r.Admin != "sudo" && r.Admin != "none" {
		return r, errors.New("Permisos de instalación no reconocidos")
	}
	switch r.Manager {
	case "apt-get", "dnf", "yum", "apk", "none":
	default:
		return r, errors.New("Gestor de paquetes no reconocido")
	}
	systemInstall := r.Admin != "none" && r.Manager != "none"
	reason := ""
	if r.Admin == "none" {
		reason = "Requiere root o sudo sin contraseña para instalar paquetes."
	} else if r.Manager == "none" {
		reason = "Instala este requisito con el administrador de paquetes de tu distribución."
	}
	r.Dependencies = []terminalDependency{
		{"tmux", "tmux · sesiones persistentes", r.Tmux, !r.Tmux && systemInstall, reason},
		{"nodejs", "Node.js y npm", r.Node && r.NPM, !(r.Node && r.NPM) && systemInstall, reason},
		{"codex", "Codex CLI", r.Codex, !r.Codex && r.NPM && r.Node, ""},
	}
	if !r.NPM || !r.Node {
		r.Dependencies[2].Reason = "Primero instala Node.js y npm."
	}
	return r, nil
}
func (r terminalRequirements) ready(kind string) bool { return r.Tmux && (kind == "shell" || r.Codex) }
func (r terminalRequirements) snapshot() string       { raw, _ := json.Marshal(r); return hashBytes(raw) }
func (a *App) terminalOptions(w http.ResponseWriter, r *http.Request) {
	s, err := a.server(r)
	if err != nil || validate(s) != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	options, err := a.terminalRequirements(r.Context(), s)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	reply(w, 200, options)
}
func (a *App) terminalInstallReview(w http.ResponseWriter, r *http.Request) {
	s, err := a.server(r)
	if err != nil || validate(s) != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	var in struct {
		Package string `json:"package"`
	}
	if decode(w, r, &in) != nil || (in.Package != "tmux" && in.Package != "nodejs" && in.Package != "codex") {
		fail(w, 400, "Paquete de terminal inválido")
		return
	}
	options, err := a.terminalRequirements(r.Context(), s)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	var dependency terminalDependency
	for _, d := range options.Dependencies {
		if d.ID == in.Package {
			dependency = d
		}
	}
	if dependency.Installed {
		fail(w, 409, "El requisito ya está instalado. Vuelve a comprobarlo")
		return
	}
	if !dependency.CanInstall {
		fail(w, 422, dependency.Reason)
		return
	}
	summary := []string{"Instalar " + dependency.Name + " en " + s.Name + ".", "Conservar las sesiones y aplicaciones existentes."}
	warnings := []string{"La descarga necesita acceso a Internet. Si falla, pueden quedar paquetes parcialmente instalados; vuelve a comprobar los requisitos."}
	if in.Package == "codex" {
		summary = append(summary, "Descargar @openai/codex desde npm e instalarlo en ~/.local para el usuario SSH. No inicia sesión en OpenAI automáticamente.")
		warnings = append(warnings, "npm ejecuta los scripts de instalación del paquete oficial. Codex puede solicitar autenticación al abrirlo.")
	} else {
		summary = append(summary, "Usar "+options.Manager+" con permisos "+options.Admin+" para instalar paquetes del sistema.")
		warnings = append(warnings, "Instalar Node.js/npm puede incorporar o actualizar sus dependencias según los repositorios de la distribución.")
	}
	p := &operationPlan{Kind: "terminal-install", Name: dependency.Name, Source: s.Name, Manager: options.Manager, Summary: summary, Warnings: warnings, Blockers: []string{}, server: s, terminalInstall: &terminalInstallPlan{in.Package, options.snapshot()}}
	a.savePlan(w, r, p)
}
func terminalInstallCommand(pkg string, r terminalRequirements) (string, error) {
	if pkg == "codex" {
		if !r.NPM || !r.Node {
			return "", errors.New("Falta Node.js/npm")
		}
		return `exec "${SHELL:-/bin/sh}" -lc ` + shellQuote(codexEnvironment+`; umask 077; npm install --global --prefix "$HOME/.local" --registry=https://registry.npmjs.org @openai/codex`), nil
	}
	packages := "tmux"
	if pkg == "nodejs" {
		packages = "nodejs npm"
	} else if pkg != "tmux" {
		return "", errors.New("Paquete no permitido")
	}
	var command string
	switch r.Manager {
	case "apt-get":
		command = "apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends " + packages
	case "dnf", "yum":
		command = r.Manager + " install -y " + packages
	case "apk":
		command = "apk add --no-cache " + packages
	default:
		return "", errors.New("Gestor de paquetes no compatible")
	}
	if r.Admin == "sudo" {
		return "sudo -n -- sh -c " + shellQuote(command), nil
	}
	if r.Admin != "root" {
		return "", errors.New("No hay permisos de instalación")
	}
	return "sh -c " + shellQuote(command), nil
}
func (a *App) runTerminalInstall(ctx context.Context, p *operationPlan) error {
	if p.terminalInstall == nil {
		return errors.New("Revisión de instalación inválida")
	}
	options, err := a.terminalRequirements(ctx, p.server)
	if err != nil {
		return err
	}
	if options.snapshot() != p.terminalInstall.Snapshot {
		return errors.New("Los requisitos cambiaron. Compruébalos y prepara una nueva revisión")
	}
	command, err := terminalInstallCommand(p.terminalInstall.Package, options)
	if err != nil {
		return err
	}
	a.operationState(p, "running", "Instalando "+p.Name+" en el servidor…")
	if err = a.stream(ctx, p.server, command, nil, io.Discard); err != nil {
		return fmt.Errorf("Falló la instalación de %s. Revisa conectividad, repositorios y permisos; después comprueba los requisitos de nuevo", p.Name)
	}
	a.operationState(p, "running", "Comprobando que el requisito esté disponible para el usuario SSH…")
	after, err := a.terminalRequirements(ctx, p.server)
	if err != nil {
		return err
	}
	for _, d := range after.Dependencies {
		if d.ID == p.terminalInstall.Package && d.Installed {
			return nil
		}
	}
	return errors.New("El instalador terminó pero el requisito no está disponible. Revisa PATH y la versión de Node.js")
}
