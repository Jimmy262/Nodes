package panel

import (
	"encoding/json"
	"errors"
	"go.yaml.in/yaml/v3"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Only inspect metadata, never environment variables or mounted application files.
const containersCommand = `ids=$(docker ps -aq) || exit; if [ -n "$ids" ]; then docker inspect --format '{"ID":{{json .Id}},"Names":{{json .Name}},"Image":{{json .Config.Image}},"Status":{{json .State.Status}},"Health":{{if (index .State "Health")}}{{json (index .State "Health").Status}}{{else}}""{{end}},"Labels":{{json .Config.Labels}},"Networks":{{json .NetworkSettings.Networks}}}' $ids; fi`

// Read only Dokploy routing files; skip the shared authentication middleware file.
const routesCommand = `if [ -d /etc/dokploy/traefik/dynamic ]; then
for f in /etc/dokploy/traefik/dynamic/*.yml /etc/dokploy/traefik/dynamic/*.yaml; do
 [ -f "$f" ] || continue
 case "$f" in */middlewares.yml|*/middlewares.yaml) continue ;; esac
 printf '\036'
 cat -- "$f" || exit 1
done
fi`

type PublicLink struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}
type Container struct {
	ID     string       `json:"ID"`
	Names  string       `json:"Names"`
	Image  string       `json:"Image"`
	Status string       `json:"Status"`
	Links  []PublicLink `json:"links"`
	Health string       `json:"health,omitempty"`
}
type inspectedContainer struct {
	Container
	Labels   map[string]string `json:"Labels"`
	Networks map[string]struct {
		Aliases  []string `json:"Aliases"`
		DNSNames []string `json:"DNSNames"`
	} `json:"Networks"`
}
type proxyRouter struct {
	Rule        string   `yaml:"rule"`
	Service     string   `yaml:"service"`
	EntryPoints []string `yaml:"entryPoints"`
	TLS         any      `yaml:"tls"`
}
type proxyConfig struct {
	HTTP struct {
		Routers  map[string]proxyRouter `yaml:"routers"`
		Services map[string]struct {
			LoadBalancer struct {
				Servers []struct {
					URL string `yaml:"url"`
				} `yaml:"servers"`
			} `yaml:"loadBalancer"`
		} `yaml:"services"`
	} `yaml:"http"`
}

var hostRulePattern = regexp.MustCompile("(?:^|[^A-Za-z0-9_])Host\\s*\\(([^)]*)\\)")
var literalPattern = regexp.MustCompile("`([^`]+)`|\"([^\"]+)\"")
var publicHostPattern = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)

func publicURL(host, scheme string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if !publicHostPattern.MatchString(host) || len(host) > 253 || !strings.Contains(host, ".") || strings.Contains(host, "..") {
		return ""
	}
	if strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()) {
		return ""
	}
	return (&url.URL{Scheme: scheme, Host: host}).String()
}
func ruleLinks(rule string, secure bool, source string) []PublicLink {
	scheme := "http"
	if secure {
		scheme = "https"
	}
	links := []PublicLink{}
	for _, match := range hostRulePattern.FindAllStringSubmatch(rule, -1) {
		for _, literal := range literalPattern.FindAllStringSubmatch(match[1], -1) {
			host := literal[1]
			if host == "" {
				host = literal[2]
			}
			if link := publicURL(host, scheme); link != "" {
				links = append(links, PublicLink{URL: link, Source: source})
			}
		}
	}
	return links
}
func secureRouter(router proxyRouter) bool {
	if router.TLS != nil {
		return true
	}
	for _, entry := range router.EntryPoints {
		if entry == "websecure" || entry == "https" {
			return true
		}
	}
	return false
}
func addLinks(container *inspectedContainer, links []PublicLink) {
	for _, link := range links {
		duplicate := false
		for _, existing := range container.Links {
			if existing.URL == link.URL {
				duplicate = true
				break
			}
		}
		if !duplicate {
			container.Links = append(container.Links, link)
		}
	}
}
func sortLinks(links []PublicLink) {
	sort.Slice(links, func(i, j int) bool {
		if strings.HasPrefix(links[i].URL, "https:") != strings.HasPrefix(links[j].URL, "https:") {
			return strings.HasPrefix(links[i].URL, "https:")
		}
		return links[i].URL < links[j].URL
	})
}
func parseContainers(out string) ([]*inspectedContainer, error) {
	items := []*inspectedContainer{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var item inspectedContainer
		if err := json.Unmarshal([]byte(line), &item); err != nil || !containerPattern.MatchString(item.ID) {
			return nil, errors.New("Docker metadata inválida")
		}
		item.Names = strings.TrimPrefix(item.Names, "/")
		item.Links = []PublicLink{}
		if item.Labels["traefik.enable"] != "false" {
			for key, value := range item.Labels {
				if !strings.HasPrefix(key, "traefik.http.routers.") || !strings.HasSuffix(key, ".rule") {
					continue
				}
				prefix := strings.TrimSuffix(key, "rule")
				secure := item.Labels[prefix+"tls"] == "true" || item.Labels[prefix+"tls.certresolver"] != ""
				for _, entry := range strings.Split(item.Labels[prefix+"entrypoints"], ",") {
					if strings.TrimSpace(entry) == "websecure" || strings.TrimSpace(entry) == "https" {
						secure = true
					}
				}
				addLinks(&item, ruleLinks(value, secure, "Traefik · Docker"))
			}
		}
		items = append(items, &item)
	}
	return items, nil
}
func (item *inspectedContainer) matches(host string) bool {
	if item.Names == host || item.Labels["com.docker.swarm.service.name"] == host {
		return true
	}
	for _, network := range item.Networks {
		for _, alias := range append(append([]string{}, network.Aliases...), network.DNSNames...) {
			if alias == host {
				return true
			}
		}
	}
	return false
}
func applyFileRoutes(items []*inspectedContainer, data string) []string {
	warnings := []string{}
	for _, document := range strings.Split(data, "\x1e") {
		if strings.TrimSpace(document) == "" {
			continue
		}
		var config proxyConfig
		if yaml.Unmarshal([]byte(document), &config) != nil {
			warnings = append(warnings, "Una configuración de Dokploy no se pudo interpretar; sus enlaces no se muestran.")
			continue
		}
		for _, router := range config.HTTP.Routers {
			links := ruleLinks(router.Rule, secureRouter(router), "Traefik · Dokploy")
			service := config.HTTP.Services[strings.TrimSuffix(router.Service, "@file")]
			for _, backend := range service.LoadBalancer.Servers {
				target, err := url.Parse(backend.URL)
				if err != nil || target.Hostname() == "" {
					continue
				}
				for _, item := range items {
					if item.matches(target.Hostname()) {
						addLinks(item, links)
					}
				}
			}
		}
	}
	return warnings
}
