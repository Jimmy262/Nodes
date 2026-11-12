package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const statsCommand = `docker stats --no-stream --no-trunc --format '{{json .}}'`

type Usage struct {
	CPU    *float64 `json:"cpu"`
	Memory *float64 `json:"memory"`
}
type Service struct {
	Container
	Usage
	Name string `json:"name"`
}
type Application struct {
	ID       string `json:"id"`
	ServerID int64  `json:"serverId"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Internal bool   `json:"internal"`
	Partial  bool   `json:"partial"`
	Usage
	Links    []PublicLink `json:"links"`
	Services []Service    `json:"services"`
}
type DockerSnapshot struct {
	Applications []Application `json:"applications"`
	At           string        `json:"at,omitempty"`
	StatsAt      string        `json:"statsAt,omitempty"`
	Error        string        `json:"error,omitempty"`
	Warnings     []string      `json:"warnings"`
	attempt      time.Time
}

// Overview reads an immutable snapshot. Opening a screen never starts SSH work.
func (a *App) overview(w http.ResponseWriter, r *http.Request) {
	servers, err := a.servers()
	if err != nil {
		fail(w, 500, "No se pudo leer el inventario")
		return
	}
	type row struct {
		Server
		State  State          `json:"state"`
		Docker DockerSnapshot `json:"docker"`
	}
	result := []row{}
	a.mu.RLock()
	for _, server := range servers {
		state, ok := a.states[server.ID]
		if !ok {
			state.Status = "pending"
		}
		docker, ok := a.docker[server.ID]
		if !ok {
			docker = DockerSnapshot{Applications: []Application{}, Warnings: []string{}}
		}
		if docker.Applications == nil {
			docker.Applications = []Application{}
		}
		if docker.Warnings == nil {
			docker.Warnings = []string{}
		}
		result = append(result, row{server, state, docker})
	}
	a.mu.RUnlock()
	names, err := a.applicationNames()
	if err != nil {
		fail(w, 500, "No se pudieron leer los nombres de aplicaciones")
		return
	}
	for i := range result {
		result[i].Docker.Applications = append([]Application{}, result[i].Docker.Applications...)
		for j := range result[i].Docker.Applications {
			if name := names[result[i].Docker.Applications[j].ID]; name != "" {
				result[i].Docker.Applications[j].Name = name
			}
		}
	}
	replySnapshot(w, r, map[string]any{"servers": result})
}
func (a *App) dockerFailed(server Server, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.sameConnection(server) {
		return
	}
	id := server.ID
	previous := a.docker[id]
	previous.Error = message
	if previous.Applications == nil {
		previous.Applications = []Application{}
	}
	if previous.Warnings == nil {
		previous.Warnings = []string{}
	}
	a.docker[id] = previous
}
func (a *App) collectDocker(ctx context.Context, server Server, cores float64) {
	// Attempt time also limits retries when Docker itself is unavailable.
	a.mu.Lock()
	if !a.sameConnection(server) {
		a.mu.Unlock()
		return
	}
	previous := a.docker[server.ID]
	previous.attempt = time.Now()
	a.docker[server.ID] = previous
	a.mu.Unlock()
	out, err := a.run(ctx, server, containersCommand)
	if err != nil {
		a.dockerFailed(server, err.Error())
		return
	}
	items, err := parseContainers(out)
	if err != nil {
		a.dockerFailed(server, err.Error())
		return
	}
	snapshot := DockerSnapshot{At: time.Now().UTC().Format(time.RFC3339Nano), Warnings: []string{}, attempt: previous.attempt}
	routes, err := a.run(ctx, server, routesCommand)
	if err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "No se pudieron actualizar los enlaces de Dokploy.")
		// Preserve previously discovered links on an exact container match only.
		for _, app := range previous.Applications {
			for _, service := range app.Services {
				for _, item := range items {
					if item.ID == service.ID {
						addLinks(item, service.Links)
					}
				}
			}
		}
	} else {
		snapshot.Warnings = append(snapshot.Warnings, applyFileRoutes(items, routes)...)
	}
	stats := map[string]Usage{}
	if len(items) > 0 {
		out, err = a.run(ctx, server, statsCommand)
		if err == nil {
			stats, err = parseStats(out, cores)
		}
		if err != nil {
			snapshot.Warnings = append(snapshot.Warnings, "Consumo no disponible: "+err.Error())
		} else {
			snapshot.StatsAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
	} else {
		snapshot.StatsAt = snapshot.At
	}
	snapshot.Applications = groupApplications(server.ID, items, stats)
	a.mu.Lock()
	if a.sameConnection(server) {
		a.docker[server.ID] = snapshot
	}
	a.mu.Unlock()
}

var sizePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*(B|kB|KB|MB|GB|TB|KiB|MiB|GiB|TiB)$`)

func parseSize(value string) (float64, error) {
	match := sizePattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, errors.New("Unidad de memoria inválida")
	}
	n, _ := strconv.ParseFloat(match[1], 64)
	multiplier := map[string]float64{"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "KiB": 1024, "MiB": 1048576, "GiB": 1073741824, "TiB": 1099511627776}[match[2]]
	n *= multiplier
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, errors.New("Memoria inválida")
	}
	return n, nil
}
func parseStats(out string, cores float64) (map[string]Usage, error) {
	result := map[string]Usage{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var stat struct{ ID, CPUPerc, MemUsage string }
		if err := json.Unmarshal([]byte(line), &stat); err != nil || !containerPattern.MatchString(stat.ID) {
			return nil, errors.New("Estadísticas Docker inválidas")
		}
		cpu, err := strconv.ParseFloat(strings.TrimSuffix(stat.CPUPerc, "%"), 64)
		if err != nil || math.IsNaN(cpu) || math.IsInf(cpu, 0) || cpu < 0 {
			return nil, errors.New("CPU Docker inválida")
		}
		used, _, ok := strings.Cut(stat.MemUsage, "/")
		if !ok {
			return nil, errors.New("Memoria Docker inválida")
		}
		memory, err := parseSize(used)
		if err != nil {
			return nil, err
		}
		usage := Usage{Memory: &memory}
		// Docker reports 100% per logical CPU. Present share of the whole host.
		if cores > 0 && !math.IsNaN(cores) && !math.IsInf(cores, 0) {
			cpu /= cores
			usage.CPU = &cpu
		}
		result[stat.ID] = usage
	}
	return result, nil
}

var deploymentSuffix = regexp.MustCompile(`-[a-z0-9]{6}$`)
var landingPrefix = regexp.MustCompile(`^landing-pages-0*[0-9]+-?`)
var numberedProject = regexp.MustCompile(`^([a-z0-9]+)-[0-9]+-(.+)$`)

func friendlyName(name string, deployment bool) string {
	if deployment {
		name = deploymentSuffix.ReplaceAllString(name, "")
	}
	name = landingPrefix.ReplaceAllString(name, "")
	if match := numberedProject.FindStringSubmatch(name); match != nil && strings.HasPrefix(match[2], match[1]) {
		name = match[2]
	}
	name = strings.ReplaceAll(name, "iacopcrm", "iacop-crm")
	words := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(name))
	for i, word := range words {
		switch strings.ToLower(word) {
		case "api", "crm", "iacop", "hqh", "mb", "sql":
			words[i] = strings.ToUpper(word)
		default:
			if len(word) > 0 {
				words[i] = strings.ToUpper(word[:1]) + word[1:]
			}
		}
	}
	return strings.Join(words, " ")
}
func preferredLinks(links []PublicLink) []PublicLink {
	byTarget := map[string]PublicLink{}
	for _, link := range links {
		u, err := url.Parse(link.URL)
		if err != nil {
			continue
		}
		key := u.Host + u.Path + "?" + u.RawQuery
		old, exists := byTarget[key]
		if !exists || strings.HasPrefix(link.URL, "https:") && !strings.HasPrefix(old.URL, "https:") {
			byTarget[key] = link
		}
	}
	out := []PublicLink{}
	for _, link := range byTarget {
		out = append(out, link)
	}
	sortLinks(out)
	// A web domain is a better entry point than its companion API.
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := url.Parse(out[i].URL)
		b, _ := url.Parse(out[j].URL)
		return !strings.HasPrefix(a.Hostname(), "api.") && strings.HasPrefix(b.Hostname(), "api.")
	})
	return out
}
func groupApplications(serverID int64, items []*inspectedContainer, stats map[string]Usage) []Application {
	groups := map[string]*Application{}
	for _, item := range items {
		key, name, deployment := "container:"+item.ID, item.Names, false
		if project := item.Labels["com.docker.compose.project"]; project != "" {
			key, name, deployment = "compose:"+project, project, true
		} else if service := item.Labels["com.docker.swarm.service.name"]; service != "" {
			key, name, deployment = "swarm:"+service, service, true
		}
		app := groups[key]
		if app == nil {
			app = &Application{ID: fmt.Sprintf("%d:%s", serverID, key), ServerID: serverID, Name: friendlyName(name, deployment), Services: []Service{}, Links: []PublicLink{}}
			groups[key] = app
		}
		usage, ok := stats[item.ID]
		if !ok && (item.Status == "exited" || item.Status == "dead" || item.Status == "created") {
			zeroCPU, zeroRAM := 0.0, 0.0
			usage = Usage{CPU: &zeroCPU, Memory: &zeroRAM}
		}
		serviceName := item.Labels["com.docker.compose.service"]
		if serviceName == "" {
			serviceName = item.Names
		}
		container := item.Container
		container.Links = preferredLinks(item.Links)
		app.Services = append(app.Services, Service{Container: container, Usage: usage, Name: serviceName})
		app.Links = append(app.Links, item.Links...)
	}
	result := []Application{}
	for _, app := range groups {
		running := 0
		unhealthy := false
		for _, service := range app.Services {
			if service.Status == "running" {
				running++
			}
			if service.Health == "unhealthy" || service.Health == "starting" {
				unhealthy = true
			}
			if service.CPU == nil || service.Memory == nil {
				app.Partial = true
			}
			if service.CPU != nil {
				if app.CPU == nil {
					app.CPU = new(float64)
				}
				*app.CPU += *service.CPU
			}
			if service.Memory != nil {
				if app.Memory == nil {
					app.Memory = new(float64)
				}
				*app.Memory += *service.Memory
			}
		}
		app.Status = "partial"
		if running == len(app.Services) && !unhealthy {
			app.Status = "running"
		} else if running == 0 {
			app.Status = "stopped"
		}
		app.Links = preferredLinks(app.Links)
		// Only infrastructure is collapsed; applications without domains stay visible.
		lower := strings.ToLower(app.Name)
		app.Internal = len(app.Links) == 0 && (strings.HasPrefix(lower, "dokploy") || lower == "traefik" || lower == "redis" || lower == "postgres")
		sort.Slice(app.Services, func(i, j int) bool { return app.Services[i].Name < app.Services[j].Name })
		result = append(result, *app)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
