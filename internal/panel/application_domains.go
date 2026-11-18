package panel

import (
	"errors"
	"strings"
)

func editDomainLabels(labels map[string]string, old editableDomain, d *domainChange, proxy proxyOptions) error {
	bases := []string{}
	for key, rule := range labels {
		if !strings.HasPrefix(key, "traefik.http.routers.") || !strings.HasSuffix(key, ".rule") {
			continue
		}
		if !strings.Contains(rule, "`"+old.Host+"`") && !strings.Contains(rule, "\""+old.Host+"\"") {
			continue
		}
		if len(ruleLinks(rule, true, "")) != 1 {
			return errors.New("Esta ruta comparte varios dominios y necesita edición manual")
		}
		bases = append(bases, strings.TrimSuffix(key, ".rule"))
	}
	if len(bases) == 0 {
		return errors.New("La ruta del dominio cambió; vuelve a abrir el editor")
	}
	if !d.Remove && old.HTTPS != d.HTTPS && (!proxy.Available || (d.HTTPS && !proxy.HTTPS)) {
		return errors.New("No se revisó la configuración de certificados del proxy")
	}
	if !d.Remove && d.Port != old.Port {
		if err := updateDomainPort(labels, old.Host, d.Port); err != nil {
			return err
		}
	}
	for _, base := range bases {
		redirect := false
		for _, middleware := range strings.Split(labels[base+".middlewares"], ",") {
			middleware = strings.TrimSpace(strings.TrimSuffix(middleware, "@docker"))
			if labels["traefik.http.middlewares."+middleware+".redirectscheme.scheme"] == "https" {
				redirect = true
			}
		}
		if d.Remove || (old.HTTPS != d.HTTPS && !d.HTTPS && redirect) {
			for key := range labels {
				if strings.HasPrefix(key, base+".") {
					delete(labels, key)
				}
			}
			continue
		}
		rule := labels[base+".rule"]
		for _, quote := range []string{"`", "\""} {
			rule = strings.ReplaceAll(rule, quote+old.Host+quote, quote+d.Host+quote)
		}
		labels[base+".rule"] = rule
		if old.HTTPS == d.HTTPS || redirect {
			continue
		}
		for key := range labels {
			if key == base+".tls" || strings.HasPrefix(key, base+".tls.") {
				delete(labels, key)
			}
		}
		labels[base+".entrypoints"] = proxy.HTTPEntry
		if d.HTTPS {
			labels[base+".entrypoints"] = proxy.HTTPSEntry
			labels[base+".tls"] = "true"
			labels[base+".tls.certresolver"] = proxy.Resolver
			name := strings.TrimPrefix(base, "traefik.http.routers.") + "-nodo-https"
			http := base + "-nodo-http"
			if _, exists := labels[http+".rule"]; exists {
				return errors.New("La ruta HTTP auxiliar ya existe")
			}
			labels[http+".rule"] = rule
			labels[http+".entrypoints"] = proxy.HTTPEntry
			if service := labels[base+".service"]; service != "" {
				labels[http+".service"] = service
			}
			labels[http+".middlewares"] = name
			labels["traefik.http.middlewares."+name+".redirectscheme.scheme"] = "https"
		}
	}
	if d.Remove {
		hasRouter := false
		for key := range labels {
			if strings.HasPrefix(key, "traefik.http.routers.") && strings.HasSuffix(key, ".rule") {
				hasRouter = true
			}
		}
		if !hasRouter {
			labels["traefik.enable"] = "false"
		}
	}
	return nil
}
