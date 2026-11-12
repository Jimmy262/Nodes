package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Static configuration is only read inside the backend. No ACME account data,
// provider credentials, container environment, or raw YAML is returned to UI.
const traefikOptionsCommand = `docker inspect --format '{"Running":{{json .State.Running}},"Cmd":{{json .Config.Cmd}},"Networks":{{json .NetworkSettings.Networks}},"Ports":{{json .NetworkSettings.Ports}}}' dokploy-traefik || exit 1
printf '\036'; cat /etc/dokploy/traefik/traefik.yml || exit 1
printf '\036'; docker network inspect --format '{"Name":{{json .Name}},"Driver":{{json .Driver}},"Attachable":{{json .Attachable}}}' dokploy-network`

type proxyOptions struct {
	Available                       bool   `json:"available"`
	HTTPS                           bool   `json:"https"`
	PublicIP                        string `json:"publicIp"`
	Message                         string `json:"message"`
	Network                         string `json:"-"`
	HTTPEntry, HTTPSEntry, Resolver string
}

// Internal routing identifiers do not need to be presented to the user.
func (p proxyOptions) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"available": p.Available, "https": p.HTTPS, "publicIp": p.PublicIP, "message": p.Message})
}

type deployAccess struct {
	Mode, Domain, Service string
	Port                  int
	HTTPS                 bool
	Proxy                 proxyOptions
}
type domainStatus struct {
	Ready      bool   `json:"ready"`
	Host       string `json:"host"`
	ExpectedIP string `json:"expectedIp"`
	Message    string `json:"message"`
}
type envValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func parseProxyOptions(raw string) proxyOptions {
	p := proxyOptions{Message: "No se detectó una configuración de Traefik compatible. Puedes desplegar por IP y puerto."}
	parts := strings.Split(raw, "\x1e")
	if len(parts) != 3 {
		return p
	}
	var container struct {
		Running  bool
		Cmd      []string
		Networks map[string]any
		Ports    map[string][]struct{ HostPort, HostIP string }
	}
	var config struct {
		Providers struct {
			Docker *struct {
				Network, Endpoint, Constraints string
				Watch                          *bool
				SwarmMode                      bool `yaml:"swarmMode"`
				UseBindPortIP                  bool `yaml:"useBindPortIP"`
			}
		}
		EntryPoints map[string]struct {
			Address string `yaml:"address"`
		} `yaml:"entryPoints"`
		CertificatesResolvers map[string]struct {
			ACME *struct {
				CAServer      string `yaml:"caServer"`
				HTTPChallenge *struct {
					EntryPoint string `yaml:"entryPoint"`
				} `yaml:"httpChallenge"`
				TLSChallenge *map[string]any `yaml:"tlsChallenge"`
			} `yaml:"acme"`
		} `yaml:"certificatesResolvers"`
	}
	var network struct {
		Name, Driver string
		Attachable   bool
	}
	if json.Unmarshal([]byte(parts[0]), &container) != nil || yaml.Unmarshal([]byte(parts[1]), &config) != nil || json.Unmarshal([]byte(parts[2]), &network) != nil {
		return p
	}
	d := config.Providers.Docker
	if !container.Running || len(container.Cmd) > 1 || d == nil || d.SwarmMode || d.UseBindPortIP || d.Constraints != "" || (d.Watch != nil && !*d.Watch) || (d.Endpoint != "" && d.Endpoint != "unix:///var/run/docker.sock") {
		return p
	}
	if d.Network != "dokploy-network" || network.Name != d.Network || (network.Driver != "bridge" && !(network.Driver == "overlay" && network.Attachable)) || container.Networks[d.Network] == nil {
		return p
	}
	entryNames := make([]string, 0, len(config.EntryPoints))
	for n := range config.EntryPoints {
		entryNames = append(entryNames, n)
	}
	sort.Strings(entryNames)
	published := func(port string) bool {
		for _, binding := range container.Ports[port+"/tcp"] {
			if binding.HostPort == port && (binding.HostIP == "" || binding.HostIP == "0.0.0.0" || binding.HostIP == "::") {
				return true
			}
		}
		return false
	}
	for _, name := range entryNames {
		entry := config.EntryPoints[name]
		if entry.Address == ":80" && published("80") {
			p.HTTPEntry = name
		}
		if entry.Address == ":443" && published("443") {
			p.HTTPSEntry = name
		}
	}
	if p.HTTPEntry == "" {
		return p
	}
	p.Network = d.Network
	p.Available = true
	p.Message = "Traefik detectado. Nodo conectará la aplicación y configurará su enlace."
	resolvers := make([]string, 0, len(config.CertificatesResolvers))
	for name := range config.CertificatesResolvers {
		resolvers = append(resolvers, name)
	}
	sort.Strings(resolvers)
	for _, name := range resolvers {
		acme := config.CertificatesResolvers[name].ACME
		if acme == nil || strings.Contains(strings.ToLower(acme.CAServer), "staging") {
			continue
		}
		if p.HTTPSEntry != "" && ((acme.HTTPChallenge != nil && acme.HTTPChallenge.EntryPoint == p.HTTPEntry) || acme.TLSChallenge != nil) {
			p.Resolver = name
			p.HTTPS = true
			break
		}
	}
	return p
}
func publicIPv4(s string) string {
	ip, e := netip.ParseAddr(s)
	if e != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return ""
	}
	for _, reserved := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"} {
		if netip.MustParsePrefix(reserved).Contains(ip) {
			return ""
		}
	}
	return ip.String()
}
func sshPublicIP(ctx context.Context, s Server) string {
	if ip := publicIPv4(s.Host); ip != "" {
		return ip
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-G", "-p", strconv.Itoa(s.Port), "-l", s.User, "--", s.Host)
	cmd.Env = sshEnvironment()
	buf := &cappedBuffer{limit: 64 << 10}
	cmd.Stdout = buf
	if cmd.Run() != nil {
		return ""
	}
	for _, line := range strings.Split(string(buf.data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "hostname" {
			if ip := publicIPv4(fields[1]); ip != "" {
				return ip
			}
			addresses, e := net.DefaultResolver.LookupNetIP(ctx, "ip4", fields[1])
			if e == nil && len(addresses) == 1 {
				return publicIPv4(addresses[0].String())
			}
			return ""
		}
	}
	return ""
}
func (a *App) proxyOptions(ctx context.Context, s Server) proxyOptions {
	raw, e := a.run(ctx, s, traefikOptionsCommand)
	if e != nil {
		return proxyOptions{Message: "No se pudo detectar Traefik. Puedes desplegar por IP y puerto o revisar la conexión."}
	}
	p := parseProxyOptions(raw)
	p.PublicIP = sshPublicIP(ctx, s)
	if p.Available && p.PublicIP == "" {
		p.Message = "Traefik detectado. No se pudo identificar una IP pública para generar el enlace automático."
	}
	return p
}
func (a *App) deploymentOptions(w http.ResponseWriter, r *http.Request) {
	s, e := a.server(r)
	if e != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	reply(w, 200, a.proxyOptions(r.Context(), s))
}
func validDomain(domain string) bool {
	if len(domain) > 253 || strings.ContainsAny(domain, "/:@*?`\"\\ \t\r\n") || !strings.Contains(domain, ".") || net.ParseIP(domain) != nil {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || !regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`).MatchString(label) {
			return false
		}
	}
	return publicURL(domain, "https") != "" && !strings.HasSuffix(domain, ".test") && !strings.HasSuffix(domain, ".invalid")
}
func domainMatchesIP(ip string, addresses []netip.Addr) bool {
	if len(addresses) == 0 {
		return false
	}
	for _, address := range addresses {
		if address.Unmap().String() != ip {
			return false
		}
	}
	return true
}
func checkDomainDNS(ctx context.Context, domain, ip string) domainStatus {
	result := domainStatus{Host: domain, ExpectedIP: ip, Message: "Crea un registro A para este dominio apuntando a " + ip + ". La propagación puede tardar; si hay un registro AAAA, comprueba que no apunte a otro servidor."}
	if ip == "" {
		result.Message = "No se pudo identificar la IP pública de este servidor para comprobar DNS."
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, e := net.DefaultResolver.LookupNetIP(ctx, "ip", domain)
	if e != nil || len(addresses) == 0 {
		return result
	}
	if !domainMatchesIP(ip, addresses) {
		result.Message = "El DNS todavía no coincide con " + ip + ". Revisa A y AAAA. Si usas un proxy DNS/CDN, desactívalo temporalmente para comprobar el origen."
		return result
	}
	result.Ready = true
	result.Message = "El DNS apunta a este servidor."
	return result
}
func (a *App) domainInUse(ctx context.Context, s Server, domain string) (bool, error) {
	metadata, e := a.run(ctx, s, containersCommand)
	if e != nil {
		return false, errors.New("No se pudieron comprobar los dominios existentes")
	}
	items, e := parseContainers(metadata)
	if e != nil {
		return false, errors.New("No se pudieron leer los dominios existentes")
	}
	matches := func(rule string) bool {
		for _, link := range ruleLinks(rule, true, "") {
			u, e := url.Parse(link.URL)
			if e == nil && strings.EqualFold(u.Hostname(), domain) {
				return true
			}
		}
		return false
	}
	for _, item := range items {
		for key, value := range item.Labels {
			if strings.HasPrefix(key, "traefik.http.routers.") && strings.HasSuffix(key, ".rule") && matches(value) {
				return true, nil
			}
		}
	}
	routes, e := a.run(ctx, s, routesCommand)
	if e != nil {
		return false, errors.New("No se pudo comprobar si Dokploy ya usa ese dominio")
	}
	for _, part := range strings.Split(routes, "\x1e") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		var config proxyConfig
		if yaml.Unmarshal([]byte(part), &config) != nil {
			return false, errors.New("Un archivo del proxy no se pudo interpretar; revisa los dominios existentes antes de desplegar")
		}
		for _, router := range config.HTTP.Routers {
			if matches(router.Rule) {
				return true, nil
			}
		}
	}
	return false, nil
}
func (a *App) prepareAccess(ctx context.Context, p *operationPlan, mode, domain, service string, port int, secure bool) error {
	if mode == "" || mode == "port" {
		return nil
	}
	if mode != "auto" && mode != "custom" {
		return errors.New("Tipo de acceso inválido")
	}
	if port < 1 || port > 65535 {
		return errors.New("Indica el puerto interno de la aplicación (1–65535)")
	}
	proxy := a.proxyOptions(ctx, p.server)
	if !proxy.Available {
		return errors.New(proxy.Message)
	}
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if mode == "auto" {
		if proxy.PublicIP == "" {
			return errors.New("No se detectó una IP pública para generar el dominio")
		}
		domain = p.project + "." + proxy.PublicIP + ".sslip.io"
	}
	if !validDomain(domain) {
		return errors.New("Escribe solo el dominio, por ejemplo app.midominio.com, sin https:// ni rutas")
	}
	p.access = &deployAccess{Mode: mode, Domain: domain, Service: service, Port: port, HTTPS: secure, Proxy: proxy}
	scheme := "http"
	if secure {
		scheme = "https"
		if !proxy.HTTPS {
			p.Blockers = append(p.Blockers, "Traefik no tiene un gestor HTTPS compatible. Desactiva HTTPS o configura sus certificados.")
		}
	}
	p.URL = scheme + "://" + domain
	dns := checkDomainDNS(ctx, domain, proxy.PublicIP)
	p.DNS = &dns
	if secure && !dns.Ready {
		p.Blockers = append(p.Blockers, "Para activar HTTPS, primero conecta el DNS y vuelve a comprobarlo.")
	}
	used, e := a.domainInUse(ctx, p.server, domain)
	if e != nil {
		p.Blockers = append(p.Blockers, e.Error())
	} else if used {
		p.Blockers = append(p.Blockers, "Ese dominio ya está configurado en este servidor. Escoge otro para evitar sustituir una aplicación.")
	}
	p.Summary = append(p.Summary, "Servicio público: "+service+" · puerto interno "+strconv.Itoa(port))
	if secure {
		p.Warnings = append(p.Warnings, "Traefik solicitará el certificado automáticamente. Los puertos 80 y 443 deben ser accesibles desde Internet; HTTPS puede tardar unos minutos.")
	}
	return nil
}
func (a *App) recheckAccess(ctx context.Context, p *operationPlan) error {
	if p.access == nil {
		return nil
	}
	current := a.proxyOptions(ctx, p.server)
	old := p.access.Proxy
	if !current.Available || current.Network != old.Network || current.HTTPEntry != old.HTTPEntry || current.HTTPSEntry != old.HTTPSEntry || current.Resolver != old.Resolver || current.PublicIP != old.PublicIP {
		return errors.New("La configuración del proxy cambió. Vuelve a revisar el despliegue")
	}
	used, e := a.domainInUse(ctx, p.server, p.access.Domain)
	if e != nil {
		return e
	}
	if used {
		return errors.New("El dominio se ocupó después de la revisión. No se ha desplegado la aplicación")
	}
	if p.access.HTTPS && !checkDomainDNS(ctx, p.access.Domain, current.PublicIP).Ready {
		return errors.New("El DNS cambió o todavía no coincide. Vuelve a revisar el dominio")
	}
	return nil
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func validateEnvironment(raw string) ([]envValue, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 64<<10 {
		return nil, errors.New("Las variables superan 64 KiB")
	}
	var env []envValue
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&env) != nil || decoder.Decode(new(any)) != io.EOF || len(env) > 100 {
		return nil, errors.New("Indica hasta 100 variables válidas")
	}
	seen := map[string]bool{}
	for _, v := range env {
		if !envNamePattern.MatchString(v.Name) || strings.HasPrefix(v.Name, "COMPOSE_") || strings.HasPrefix(v.Name, "DOCKER_") {
			return nil, errors.New("Nombre de variable inválido o reservado: " + v.Name)
		}
		if seen[v.Name] {
			return nil, errors.New("La variable " + v.Name + " está repetida")
		}
		seen[v.Name] = true
		if strings.ContainsRune(v.Value, 0) || len(v.Value) > 8192 {
			return nil, errors.New("Valor inválido o demasiado largo para " + v.Name)
		}
	}
	return env, nil
}

// Values are double-quoted dotenv strings. Escape dollars before encoding to
// prevent Compose from expanding secrets containing $NAME or ${NAME}.
func environmentFile(env []envValue) []byte {
	var out strings.Builder
	escape := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\r", "\\r", "\n", "\\n", "\t", "\\t", "$", "$$")
	for _, v := range env {
		fmt.Fprintf(&out, "%s=\"%s\"\n", v.Name, escape.Replace(v.Value))
	}
	return []byte(out.String())
}
func stringMapping(raw any) (map[string]any, error) {
	if raw == nil {
		return map[string]any{}, nil
	}
	if m, ok := raw.(map[string]any); ok {
		return m, nil
	}
	if list, ok := raw.([]any); ok {
		m := map[string]any{}
		for _, entry := range list {
			s, ok := entry.(string)
			if !ok {
				return nil, errors.New("Formato de variables o etiquetas no válido")
			}
			key, value, has := strings.Cut(s, "=")
			if has {
				m[key] = value
			} else {
				m[key] = nil
			}
		}
		return m, nil
	}
	return nil, errors.New("Formato de variables o etiquetas no válido")
}
func configureDeployment(z *zipProject, p *operationPlan, env []envValue, envTarget string) error {
	services := z.config["services"].(map[string]any)
	if envTarget == "" {
		if len(z.services) == 1 {
			envTarget = z.services[0]
		} else if p.access != nil {
			envTarget = p.access.Service
		}
	}
	if len(env) > 0 && envTarget != "*" && services[envTarget] == nil {
		return errors.New("Escoge el servicio que recibirá las variables")
	}
	if p.access != nil && services[p.access.Service] == nil {
		return errors.New("Escoge el servicio que debe responder al dominio")
	}
	for _, name := range z.services {
		service := services[name].(map[string]any)
		if z.dockerfile && z.hasDotEnv {
			service["env_file"] = ".env"
		}
		if len(env) > 0 && (envTarget == "*" || envTarget == name) {
			m, e := stringMapping(service["environment"])
			if e != nil {
				return e
			}
			for _, v := range env {
				m[v.Name] = strings.ReplaceAll(v.Value, "$", "$$")
			}
			service["environment"] = m
		}
		// The explicitly selected access mode owns Traefik routing for this new
		// deployment. Original labels cannot silently publish another service.
		labels, e := stringMapping(service["labels"])
		if e != nil {
			return e
		}
		for key := range labels {
			if strings.HasPrefix(key, "traefik.") {
				delete(labels, key)
			}
		}
		labels["traefik.enable"] = "false"
		service["labels"] = labels
		if p.access != nil {
			delete(service, "ports")
		}
	}
	if p.access != nil {
		access := p.access
		service := services[access.Service].(map[string]any)
		labels := service["labels"].(map[string]any)
		router := "nodo-" + p.ID[:16]
		labels["traefik.enable"] = "true"
		labels["traefik.docker.network"] = access.Proxy.Network
		base := "traefik.http.routers." + router
		labels[base+".rule"] = "Host(`" + access.Domain + "`)"
		labels[base+".service"] = router
		labels["traefik.http.services."+router+".loadbalancer.server.port"] = strconv.Itoa(access.Port)
		if access.HTTPS {
			labels[base+".entrypoints"] = access.Proxy.HTTPSEntry
			labels[base+".tls"] = "true"
			labels[base+".tls.certresolver"] = access.Proxy.Resolver
			labels[base+"-http.rule"] = labels[base+".rule"]
			labels[base+"-http.entrypoints"] = access.Proxy.HTTPEntry
			labels[base+"-http.service"] = router
			labels[base+"-http.middlewares"] = router + "-https"
			labels["traefik.http.middlewares."+router+"-https.redirectscheme.scheme"] = "https"
		} else {
			labels[base+".entrypoints"] = access.Proxy.HTTPEntry
		}
		networks, _ := z.config["networks"].(map[string]any)
		if networks == nil {
			networks = map[string]any{}
		}
		networkKey := "nodo_proxy_" + p.ID[:8]
		networks[networkKey] = map[string]any{"external": true, "name": access.Proxy.Network}
		switch existing := service["networks"].(type) {
		case nil:
			service["networks"] = []any{"default", networkKey}
			if networks["default"] == nil {
				networks["default"] = map[string]any{}
			}
		case []any:
			service["networks"] = append(existing, networkKey)
		case map[string]any:
			existing[networkKey] = map[string]any{}
		default:
			return errors.New("La lista de redes del servicio no es válida")
		}
		z.config["networks"] = networks
		p.Summary = append(p.Summary, "Solo el servicio elegido será público a través de Traefik; los demás servicios usarán sus redes internas.")
	}
	z.environmentFile = environmentFile(env)
	for _, v := range env {
		p.EnvironmentKeys = append(p.EnvironmentKeys, v.Name)
	}
	sort.Strings(p.EnvironmentKeys)
	if len(env) > 0 {
		p.Summary = append(p.Summary, fmt.Sprintf("%d variable(s) · %s", len(env), map[bool]string{true: "todos los servicios", false: envTarget}[envTarget == "*"]))
	}
	var e error
	z.runtime, e = json.Marshal(z.config)
	return e
}
