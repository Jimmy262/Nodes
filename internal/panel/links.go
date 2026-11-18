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
	ID             string       `json:"ID"`
	Names          string       `json:"Names"`
	Image          string       `json:"Image"`
	Status         string       `json:"Status"`
	Links          []PublicLink `json:"links"`
	Health         string       `json:"health,omitempty"`
	LinksTruncated bool         `json:"linksTruncated,omitempty"`
}
type inspectedContainer struct {
	Container
	linkIndex  map[string]int
	linkBudget *linkBudget
	Labels     map[string]string `json:"Labels"`
	Networks   map[string]struct {
		Aliases  []string `json:"Aliases"`
		DNSNames []string `json:"DNSNames"`
	} `json:"Networks"`
}

// Bound derived inventory data independently of the SSH response byte limit.
// The work budget also covers duplicate associations and cached-link restores.
const maxRuleLinks = 64
const maxContainerLinks = 64
const maxInventoryLinks = 4096
const maxLinkAttempts = 65536
const truncatedLinksWarning = "Se alcanzó el límite de detección de enlaces; algunos enlaces no se muestran."

type linkBudget struct {
	links     int
	attempts  int
	truncated bool
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

// Inventory extraction is deliberately separate: editing and migration callers
// of ruleLinks must still see the complete rule when validating configuration.
func inventoryRuleLinks(rule string, secure bool, source string) ([]PublicLink, bool) {
	scheme := "http"
	if secure {
		scheme = "https"
	}
	links := []PublicLink{}
	seen := map[string]bool{}
	for offset := 0; offset < len(rule); {
		match := hostRulePattern.FindStringSubmatchIndex(rule[offset:])
		if match == nil {
			break
		}
		literals := rule[offset+match[2] : offset+match[3]]
		offset += match[1]
		for len(literals) > 0 {
			literal := literalPattern.FindStringSubmatchIndex(literals)
			if literal == nil {
				break
			}
			start, end := literal[2], literal[3]
			if start < 0 {
				start, end = literal[4], literal[5]
			}
			link := publicURL(literals[start:end], scheme)
			literals = literals[literal[1]:]
			if link == "" || seen[link] {
				continue
			}
			if len(links) == maxRuleLinks {
				return links, true
			}
			seen[link] = true
			links = append(links, PublicLink{URL: link, Source: source})
		}
	}
	return links, false
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
	if container.linkBudget == nil {
		container.linkBudget = &linkBudget{links: len(container.Links)}
	}
	if container.linkIndex == nil {
		container.linkIndex = map[string]int{}
		for i, link := range container.Links {
			container.linkIndex[link.URL] = i
		}
	}
	budget := container.linkBudget
	for _, link := range links {
		if budget.attempts >= maxLinkAttempts {
			container.LinksTruncated, budget.truncated = true, true
			return
		}
		budget.attempts++
		if _, duplicate := container.linkIndex[link.URL]; duplicate {
			continue
		}
		// An HTTPS upgrade remains possible even after cardinality is exhausted.
		if strings.HasPrefix(link.URL, "https:") {
			httpURL := "http:" + strings.TrimPrefix(link.URL, "https:")
			if i, ok := container.linkIndex[httpURL]; ok && (len(container.Links) >= maxContainerLinks || budget.links >= maxInventoryLinks) {
				delete(container.linkIndex, httpURL)
				container.Links[i] = link
				container.linkIndex[link.URL] = i
				continue
			}
		}
		if len(container.Links) >= maxContainerLinks || budget.links >= maxInventoryLinks {
			container.LinksTruncated, budget.truncated = true, true
			continue
		}
		container.linkIndex[link.URL] = len(container.Links)
		container.Links = append(container.Links, link)
		budget.links++
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
	budget := &linkBudget{}
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
		item.LinksTruncated = false
		item.linkBudget = budget
		if item.Labels["traefik.enable"] != "false" {
			keys := make([]string, 0, len(item.Labels))
			for key := range item.Labels {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
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
				if budget.attempts >= maxLinkAttempts {
					item.LinksTruncated, budget.truncated = true, true
					break
				}
				links, truncated := inventoryRuleLinks(item.Labels[key], secure, "Traefik · Docker")
				item.LinksTruncated = item.LinksTruncated || truncated
				budget.truncated = budget.truncated || truncated
				addLinks(&item, links)
			}
		}
		items = append(items, &item)
	}
	return items, nil
}
func applyFileRoutes(items []*inspectedContainer, data string) []string {
	warnings := []string{}
	budget := &linkBudget{}
	if len(items) > 0 && items[0].linkBudget != nil {
		budget = items[0].linkBudget
	}
	byHost := map[string][]*inspectedContainer{}
	for _, item := range items {
		if item.linkBudget != budget {
			budget.links += len(item.Links)
			item.linkBudget = budget
		}
		seen := map[string]bool{}
		index := func(host string) {
			if host != "" && !seen[host] {
				seen[host] = true
				byHost[host] = append(byHost[host], item)
			}
		}
		index(item.Names)
		index(item.Labels["com.docker.swarm.service.name"])
		for _, network := range item.Networks {
			for _, alias := range network.Aliases {
				index(alias)
			}
			for _, name := range network.DNSNames {
				index(name)
			}
		}
	}
	// Warnings use the existing API channel; the per-container flag also covers
	// Docker labels when the subsequent route read fails.
	finish := func() []string {
		if budget.truncated {
			warnings = append(warnings, truncatedLinksWarning)
		}
		return warnings
	}
	for _, document := range strings.Split(data, "\x1e") {
		if strings.TrimSpace(document) == "" {
			continue
		}
		var config proxyConfig
		if yaml.Unmarshal([]byte(document), &config) != nil {
			warnings = append(warnings, "Una configuración de Dokploy no se pudo interpretar; sus enlaces no se muestran.")
			continue
		}
		routerNames := make([]string, 0, len(config.HTTP.Routers))
		for name := range config.HTTP.Routers {
			routerNames = append(routerNames, name)
		}
		sort.Strings(routerNames)
		for _, name := range routerNames {
			if budget.attempts >= maxLinkAttempts {
				budget.truncated = true
				return finish()
			}
			router := config.HTTP.Routers[name]
			links, truncated := inventoryRuleLinks(router.Rule, secureRouter(router), "Traefik · Dokploy")
			budget.truncated = budget.truncated || truncated
			if len(links) == 0 {
				continue
			}
			service := config.HTTP.Services[strings.TrimSuffix(router.Service, "@file")]
			seenTargets := map[string]bool{}
			for _, backend := range service.LoadBalancer.Servers {
				// Count backend probes as well as successful associations: many
				// routers can reference one service whose targets match nothing.
				if budget.attempts >= maxLinkAttempts {
					budget.truncated = true
					return finish()
				}
				budget.attempts++
				target, err := url.Parse(backend.URL)
				if err != nil || target.Hostname() == "" {
					continue
				}
				host := target.Hostname()
				if seenTargets[host] {
					continue
				}
				seenTargets[host] = true
				for _, item := range byHost[host] {
					item.LinksTruncated = item.LinksTruncated || truncated
					addLinks(item, links)
					if budget.attempts >= maxLinkAttempts {
						budget.truncated = true
						return finish()
					}
				}
			}
		}
	}
	return finish()
}
