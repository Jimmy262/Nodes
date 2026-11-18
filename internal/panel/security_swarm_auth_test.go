package panel

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const securitySwarmProxyFixture = `{"Running":true,"Cmd":["traefik"],"Networks":{"dokploy-network":{}},"Ports":{"80/tcp":[{"HostPort":"80","HostIP":"0.0.0.0"}],"443/tcp":[{"HostPort":"443","HostIP":"0.0.0.0"}]}}` + "\x1e" + `
providers:
  docker:
    network: dokploy-network
    exposedByDefault: false
    watch: true
entryPoints:
  web:
    address: :80
  websecure:
    address: :443
  private:
    address: 127.0.0.1:8443
certificatesResolvers:
  letsencrypt:
    acme:
      email: dummy@example.com
      httpChallenge:
        entryPoint: web
` + "\x1e" + `{"Name":"dokploy-network","Driver":"overlay","Attachable":true}`

// All remote reads use dummy data. No Docker, SSH, DNS or streaming operation
// is permitted by this fixture, including when preparing the complete plan.
func securityAdoptedSwarmFixture(t *testing.T) (*App, *operationPlan, *swarmService) {
	t.Helper()
	t.Setenv("NODO_MASTER_KEY", strings.Repeat("5a", 32))
	a, err := New(filepath.Join(t.TempDir(), "dummy.db"), strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	source := Server{ID: 1, Name: "source", Host: "8.8.8.8", Port: 22, User: "dummy"}
	target := Server{ID: 2, Name: "target", Host: "1.1.1.1", Port: 22, User: "dummy"}
	for _, s := range []Server{source, target} {
		if _, err := a.db.Exec("INSERT INTO servers VALUES(?,?,?,?,?,?)", s.ID, s.Name, s.Host, s.Port, s.User, ""); err != nil {
			t.Fatal(err)
		}
	}
	c := &containerSpec{ID: strings.Repeat("b", 64), Config: map[string]any{"Labels": map[string]any{"com.docker.swarm.service.name": "example"}}}
	c.State.Running = true
	var service swarmService
	if err := json.Unmarshal([]byte(`{"ID":"dummy-source","Spec":{"Name":"example","Labels":{},"Mode":{"Replicated":{"Replicas":1}},"TaskTemplate":{"Networks":[{"Target":"source-network"}],"ContainerSpec":{"Image":"dummy:latest","Labels":{},"Mounts":[]}}}}`), &service); err != nil {
		t.Fatal(err)
	}
	p := &operationPlan{ID: strings.Repeat("a", 64), Kind: "migrate", server: source, target: target, container: c}
	p.swarm = &swarmMigration{Source: service, TargetNetwork: "source-network", Domain: "dummy.example.com", Port: 4321, Proxy: parseProxyOptions(securitySwarmProxyFixture)}
	service.Spec = targetSwarmSpec(p, "dummy:latest", nil)
	object(object(service.Spec, "Mode"), "Replicated")["Replicas"] = float64(1)
	// A real row selects the adopted-service branch. Its encrypted payload is
	// irrelevant to migration's current existence-only lookup.
	if _, err := a.db.Exec("INSERT INTO native_swarm_apps VALUES(?,?,?,?,?,?,?,?)", source.ID, "example", p.ID, connectionHash(source), []byte("dummy-key"), []byte("dummy-definition"), "active", "dummy-time"); err != nil {
		t.Fatal(err)
	}
	a.swarmDNS = func(_ context.Context, host, ip string) domainStatus {
		return domainStatus{Ready: true, Host: host, ExpectedIP: ip}
	}
	a.stream = func(context.Context, Server, string, io.Reader, io.Writer) error {
		t.Fatal("unexpected mutation")
		return nil
	}
	a.run = func(_ context.Context, s Server, cmd string) (string, error) {
		encode := func(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }
		switch {
		case strings.HasPrefix(cmd, "docker service inspect --format '{{json .}}'"):
			return encode(service)
		case strings.HasPrefix(cmd, "docker info --format '{\"id\""):
			return encode(map[string]string{"id": strconv.FormatInt(s.ID, 10), "os": "linux", "arch": "x86_64"})
		case strings.HasPrefix(cmd, "docker version --format"):
			return "1.52", nil
		case cmd == swarmTopologyCommand:
			return encode(map[string]any{"State": "active", "Control": true, "Nodes": 1, "Cluster": strconv.FormatInt(s.ID, 10)})
		case strings.HasPrefix(cmd, "docker network inspect dokploy-network --format"):
			id := "source-network"
			if s.ID == target.ID {
				id = "target-network"
			}
			return encode(map[string]any{"ID": id, "Driver": "overlay", "Scope": "swarm", "Attachable": true})
		case strings.Contains(cmd, "command -v curl"), cmd == swarmVolumesCommand, cmd == containersCommand, cmd == routesCommand:
			return "", nil
		case cmd == traefikOptionsCommand:
			return securitySwarmProxyFixture, nil
		default:
			t.Fatalf("unexpected command: %s", cmd)
			return "", nil
		}
	}
	return a, p, &service
}

func TestSecuritySwarmRejectsUnsupportedNativeRoutePolicy(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any, map[string]any, string)
	}{
		{"basic-auth", func(_ map[string]any, labels map[string]any, base string) {
			labels[base+".middlewares"] = "dummy-auth"
			labels["traefik.http.middlewares.dummy-auth.basicauth.users"] = "dummy:dummy"
		}},
		{"external-auth", func(_ map[string]any, labels map[string]any, base string) { labels[base+".middlewares"] = "auth@file" }},
		{"unproven-redirect-name", func(_ map[string]any, labels map[string]any, base string) {
			labels[base+".middlewares"] = "redirect-to-https"
		}},
		{"forged-external-redirect", func(_ map[string]any, labels map[string]any, base string) {
			labels[base+".middlewares"] = "auth@file"
			labels["traefik.http.middlewares.auth@file.redirectscheme.scheme"] = "https"
		}},
		{"redirect-name-with-auth", func(_ map[string]any, labels map[string]any, base string) {
			labels[base+".middlewares"] = "redirect-to-https"
			labels["traefik.http.middlewares.redirect-to-https.basicauth.users"] = "dummy:dummy"
		}},
		{"redirect-with-auth-option", func(_ map[string]any, labels map[string]any, base string) {
			labels["traefik.http.middlewares.nodo-"+strings.Repeat("a", 16)+"-https.forwardauth.address"] = "http://dummy.invalid"
		}},
		{"auth-chain", func(_ map[string]any, labels map[string]any, base string) {
			labels[base+".middlewares"] = "gate"
			labels["traefik.http.middlewares.gate.chain.middlewares"] = "dummy-auth"
		}},
		{"path-rule", func(_ map[string]any, labels map[string]any, base string) {
			labels[base+".rule"] = "Host(`dummy.example.com`) && PathPrefix(`/private`)"
		}},
		{"tls-policy", func(_ map[string]any, labels map[string]any, base string) {
			labels[base+".tls.options"] = "require-client-cert@file"
		}},
		{"router-priority", func(_ map[string]any, labels map[string]any, base string) { labels[base+".priority"] = "999" }},
		{"private-entrypoint", func(_ map[string]any, labels map[string]any, base string) {
			labels[base+".entrypoints"] = "private"
		}},
		{"missing-entrypoint", func(_ map[string]any, labels map[string]any, base string) {
			delete(labels, base+".entrypoints")
		}},
		{"multiple-entrypoints", func(_ map[string]any, labels map[string]any, base string) {
			labels[base+".entrypoints"] = "websecure,private"
		}},
		{"wrong-tls-entrypoint", func(_ map[string]any, labels map[string]any, base string) {
			labels[base+".entrypoints"] = "web"
		}},
		{"service-provider-auth", func(spec map[string]any, _ map[string]any, base string) {
			object(spec, "Labels")[base+".middlewares"] = "dummy-auth@file"
		}},
		{"service-provider-port", func(spec map[string]any, _ map[string]any, _ string) {
			object(spec, "Labels")["traefik.http.services.other.loadbalancer.server.port"] = "4321"
		}},
		{"tcp-route", func(_ map[string]any, labels map[string]any, _ string) {
			labels["traefik.tcp.routers.private.rule"] = "HostSNI(`*`)"
		}},
		{"case-variant-policy", func(_ map[string]any, labels map[string]any, base string) {
			labels[strings.Replace(base, "traefik", "Traefik", 1)+".middlewares"] = "auth@file"
		}},
		{"disabled-route", func(_ map[string]any, labels map[string]any, _ string) { labels["traefik.enable"] = "false" }},
		{"missing-native-marker", func(_ map[string]any, labels map[string]any, _ string) { delete(labels, "lat.nodo.managed") }},
		{"external-service", func(_ map[string]any, labels map[string]any, base string) { labels[base+".service"] = "private@file" }},
		{"multiple-ports", func(_ map[string]any, labels map[string]any, _ string) {
			labels["traefik.http.services.other.loadbalancer.server.port"] = "4322"
		}},
		{"non-string-option", func(_ map[string]any, labels map[string]any, base string) { labels[base+".middlewares"] = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, p, service := securityAdoptedSwarmFixture(t)
			labels := object(object(object(service.Spec, "TaskTemplate"), "ContainerSpec"), "Labels")
			tc.change(service.Spec, labels, "traefik.http.routers.nodo-"+p.ID[:16])
			before := specHash(service.Spec)
			a.planManagedMigration(context.Background(), p)
			if len(p.Blockers) == 0 || p.Review != nil || p.URL != "" {
				t.Fatal("unsupported source policy became a reviewable migration")
			}
			if specHash(service.Spec) != before {
				t.Fatal("source policy was modified")
			}
		})
	}
}

func TestSecuritySwarmRejectsInheritedSourceEntryPolicy(t *testing.T) {
	for _, policy := range []string{
		"    http:\n      middlewares: [auth@file]\n",
		"    http:\n      tls:\n        options: require-client-cert@file\n",
		"    HTTP:\n      Middlewares: [auth@file]\n",
		"    http:\n      redirections:\n        entryPoint:\n          to: private\n",
	} {
		t.Run(policy, func(t *testing.T) {
			a, p, _ := securityAdoptedSwarmFixture(t)
			original := a.run
			a.run = func(ctx context.Context, s Server, cmd string) (string, error) {
				if s.ID == p.server.ID && cmd == traefikOptionsCommand {
					return strings.Replace(securitySwarmProxyFixture, "  websecure:\n    address: :443\n", "  websecure:\n    address: :443\n"+policy, 1), nil
				}
				return original(ctx, s, cmd)
			}
			a.planManagedMigration(context.Background(), p)
			if len(p.Blockers) == 0 || p.Review != nil || p.URL != "" {
				t.Fatal("inherited source access policy became a public migration")
			}
		})
	}
}

func TestSecuritySwarmAllowsGeneratedRedirectRoute(t *testing.T) {
	a, p, service := securityAdoptedSwarmFixture(t)
	before := specHash(service.Spec)
	a.planManagedMigration(context.Background(), p)
	if len(p.Blockers) != 0 || p.Review == nil || p.swarm.Port != 4321 {
		t.Fatalf("ordinary native migration blocked: %v", p.Blockers)
	}
	target := targetSwarmSpec(p, "dummy:copied", nil)
	if port, err := nativeSwarmRoutePort(target, p.swarm.Proxy); err != nil || port != 4321 {
		t.Fatalf("generated target route rejected: port=%d err=%v", port, err)
	}
	if specHash(service.Spec) != before {
		t.Fatal("source route was modified")
	}
}
