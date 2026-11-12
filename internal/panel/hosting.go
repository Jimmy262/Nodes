package panel

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Explicit origin, never inferred from untrusted forwarding headers.
func (a *App) ConfigurePublicURL(raw string) error {
	if raw == "" {
		a.publicOrigin = ""
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("public-url debe ser un origen HTTPS, por ejemplo https://nodo.ejemplo.com")
	}
	a.publicOrigin = "https://" + u.Host
	return nil
}
func (a *App) requestOrigin(r *http.Request) string {
	if a.publicOrigin != "" {
		return a.publicOrigin
	}
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}
func (a *App) secureRequest(r *http.Request) bool { return r.TLS != nil || a.publicOrigin != "" }
func (a *App) allowedHost(r *http.Request, host string, splitErr error) bool {
	if a.publicOrigin != "" {
		if "https://"+r.Host != a.publicOrigin {
			return false
		}
		if r.TLS == nil {
			peer, _, err := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(peer)
			if err != nil || ip == nil || !ip.IsLoopback() {
				return false
			}
		}
		return true
	}
	return splitErr == nil && (host == "127.0.0.1" || host == "localhost" || strings.Trim(host, "[]") == "::1")
}
