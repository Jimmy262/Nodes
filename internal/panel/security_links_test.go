package panel

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Dummy metadata only: no SSH, Docker, filesystem, or network operations.
func securityLinkFixture(n, hosts int) (string, string) {
	var metadata strings.Builder
	for i := 0; i < n; i++ {
		item := map[string]any{
			"ID": fmt.Sprintf("%064x", i+1), "Names": fmt.Sprintf("/app-%d", i),
			"Labels":   map[string]string{},
			"Networks": map[string]any{"net": map[string]any{"Aliases": []string{"shared"}}},
		}
		b, _ := json.Marshal(item)
		metadata.Write(b)
		metadata.WriteByte('\n')
	}
	var rule strings.Builder
	rule.WriteString("Host(")
	for i := 0; i < hosts; i++ {
		if i != 0 {
			rule.WriteByte(',')
		}
		fmt.Fprintf(&rule, "`h%04d.example.com`", i)
	}
	rule.WriteByte(')')
	routes := fmt.Sprintf("http:\n  routers:\n    app:\n      rule: '%s'\n      service: app\n      tls: {}\n  services:\n    app:\n      loadBalancer:\n        servers: [{url: 'http://shared:80'}]\n", rule.String())
	return metadata.String(), routes
}

func TestSecurityLinkFanoutBoundedFixture(t *testing.T) {
	for _, n := range []int{4, 8} {
		for _, hosts := range []int{16, 32, 64} {
			t.Run(fmt.Sprintf("N%d-L%d", n, hosts), func(t *testing.T) {
				metadata, routes := securityLinkFixture(n, hosts)
				var total int
				allocs := testing.AllocsPerRun(1, func() {
					items, err := parseContainers(metadata)
					if err != nil {
						t.Fatal(err)
					}
					if warnings := applyFileRoutes(items, routes); len(warnings) != 0 {
						t.Fatal(warnings)
					}
					total = 0
					for _, item := range items {
						total += len(item.Links)
					}
				})
				if total != n*hosts {
					t.Fatalf("links=%d, want %d", total, n*hosts)
				}
				t.Logf("metadata_bytes=%d routes_bytes=%d links=%d allocs=%.0f", len(metadata), len(routes), total, allocs)
			})
		}
	}
}

func TestSecurityLinkRuleLimitPreservesCompleteValidation(t *testing.T) {
	_, routes := securityLinkFixture(4, maxRuleLinks+1)
	start := strings.Index(routes, "Host(")
	end := strings.Index(routes[start:], ")") + start + 1
	rule := routes[start:end]
	links, truncated := inventoryRuleLinks(rule, true, "test")
	if len(links) != maxRuleLinks || !truncated {
		t.Fatalf("links=%d truncated=%v", len(links), truncated)
	}
	if len(ruleLinks(rule, true, "test")) != maxRuleLinks+1 {
		t.Fatal("configuration validation was truncated")
	}
	// Invalid and duplicate literals do not consume distinct-link capacity.
	links, truncated = inventoryRuleLinks("Host(`localhost`, `a.example.com`, `a.example.com`)", true, "test")
	if len(links) != 1 || truncated {
		t.Fatalf("links=%v truncated=%v", links, truncated)
	}
	metadata, _ := json.Marshal(map[string]any{
		"ID": strings.Repeat("a", 64), "Names": "/app",
		"Labels": map[string]string{"traefik.http.routers.app.rule": rule},
	})
	items, err := parseContainers(string(metadata))
	if err != nil || len(items) != 1 {
		t.Fatalf("parse error: %v", err)
	}
	if len(items[0].Links) != maxRuleLinks || !items[0].LinksTruncated {
		t.Fatal("Docker-label rule limit not signaled")
	}
	if warnings := applyFileRoutes(items, ""); len(warnings) != 1 {
		t.Fatal("Docker-label truncation warning missing")
	}
}

func TestSecurityLinkContainerLimitAndHTTPSUpgrade(t *testing.T) {
	item := &inspectedContainer{}
	for i := 0; i < maxContainerLinks; i++ {
		addLinks(item, []PublicLink{{URL: fmt.Sprintf("http://h%d.example.com", i), Source: "Docker"}})
	}
	addLinks(item, []PublicLink{{URL: "https://h0.example.com", Source: "Dokploy"}})
	if len(item.Links) != maxContainerLinks || item.Links[0].URL != "https://h0.example.com" {
		t.Fatalf("HTTPS upgrade lost: %v", item.Links)
	}
	addLinks(item, []PublicLink{{URL: "http://extra.example.com"}})
	if !item.LinksTruncated || len(item.Links) != maxContainerLinks {
		t.Fatal("container cap not signaled")
	}
	addLinks(item, []PublicLink{item.Links[0]})
	if len(item.Links) != maxContainerLinks {
		t.Fatal("duplicate appended")
	}
}

func TestSecurityLinkAggregateAndWorkBudgets(t *testing.T) {
	metadata, routes := securityLinkFixture(4, 16)
	items, err := parseContainers(metadata)
	if err != nil {
		t.Fatal(err)
	}
	budget := items[0].linkBudget
	// Preload accounting to exercise the production threshold with tiny input.
	budget.links = maxInventoryLinks - 1
	warnings := applyFileRoutes(items, routes)
	total := 0
	for _, item := range items {
		total += len(item.Links)
	}
	if total != 1 || budget.links != maxInventoryLinks || len(warnings) != 1 {
		t.Fatalf("links=%d budget=%+v warnings=%v", total, budget, warnings)
	}
	items, _ = parseContainers(metadata)
	budget = items[0].linkBudget
	budget.attempts = maxLinkAttempts - 1
	warnings = applyFileRoutes(items, routes)
	if budget.attempts != maxLinkAttempts || len(warnings) != 1 || !items[0].LinksTruncated {
		t.Fatalf("budget=%+v warnings=%v", budget, warnings)
	}
}

func TestSecurityLinkDuplicateTargetsAndTruncationSignal(t *testing.T) {
	metadata, routes := securityLinkFixture(4, 16)
	items, _ := parseContainers(metadata)
	routes = strings.Replace(routes, "servers: [{url: 'http://shared:80'}]", "servers: [{url: 'http://shared:80'}, {url: 'http://shared:81'}]", 1)
	if warnings := applyFileRoutes(items, routes); len(warnings) != 0 {
		t.Fatal(warnings)
	}
	if items[0].linkBudget.attempts != 4*16+2 {
		t.Fatalf("duplicate backend amplified work: %d", items[0].linkBudget.attempts)
	}
	// Unmatched backends also consume work, even without derived links.
	items, _ = parseContainers(metadata)
	items[0].linkBudget.attempts = maxLinkAttempts - 1
	unmatched := strings.ReplaceAll(routes, "http://shared:", "http://unmatched:")
	if warnings := applyFileRoutes(items, unmatched); len(warnings) != 1 || items[0].linkBudget.attempts != maxLinkAttempts {
		t.Fatalf("unmatched backend budget ignored: %v", warnings)
	}
	_, routes = securityLinkFixture(4, maxRuleLinks+1)
	items, _ = parseContainers(metadata)
	if warnings := applyFileRoutes(items, routes); len(warnings) != 1 {
		t.Fatal(warnings)
	}
	encoded, _ := json.Marshal(items[0].Container)
	if !strings.Contains(string(encoded), `"linksTruncated":true`) {
		t.Fatalf("truncation not exposed: %s", encoded)
	}
}
