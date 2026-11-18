package panel

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type onboardingTransport func(*http.Request) (*http.Response, error)

func (f onboardingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGithubFirstDeployUsesReviewedCommitWithReadOnlyToken(t *testing.T) {
	t.Setenv("NODO_MASTER_KEY", strings.Repeat("5a", 32))
	a, err := New(filepath.Join(t.TempDir(), "nodes.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	protected, encrypted, err := sealDokployKey(githubBinding, []byte("demo-read-only-token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec("INSERT INTO github_connection VALUES(1,'demo',?,?)", protected, encrypted); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec("INSERT INTO servers VALUES(1,'VPS','example.invalid',22,'deploy','')"); err != nil {
		t.Fatal(err)
	}
	const repo = "/repos/demo/site"
	commit := strings.Repeat("a", 40)
	zip := securityComposeZip(t, map[string]string{"root/web/compose.yml": "services: {web: {image: nginx, ports: ['8080:80']}}"})
	var paths []string
	a.githubHTTP = &http.Client{Transport: onboardingTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Fatalf("first deploy sent %s to GitHub", r.Method)
		}
		paths = append(paths, r.URL.Path)
		var body []byte
		switch r.URL.Path {
		case repo + "/commits/main":
			body = []byte(`{"sha":"` + commit + `"}`)
		case repo + "/zipball/" + commit:
			body = zip
		case repo + "/tarball/" + commit:
			body = []byte("reviewed archive")
		default:
			t.Fatalf("first deploy requested %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	a.run = func(context.Context, Server, string) (string, error) { return "", nil }
	var installed, deployed bool
	a.stream = func(_ context.Context, _ Server, command string, input io.Reader, _ io.Writer) error {
		if strings.Contains(command, "tar -xf") {
			archive := tar.NewReader(input)
			for {
				header, err := archive.Next()
				if err != nil {
					t.Fatalf("read installer policy: %v", err)
				}
				if header.Name == "policy.json" {
					var policy struct{ BuildPath, Compose string }
					if err := json.NewDecoder(archive).Decode(&policy); err != nil || policy.BuildPath != "web" || policy.Compose != "compose.yml" {
						t.Fatalf("reviewed project directory not retained: %+v, %v", policy, err)
					}
					installed = true
					return nil
				}
			}
		}
		data, err := io.ReadAll(input)
		if err != nil || !installed || !strings.HasSuffix(command, commit) || string(data) != "reviewed archive" {
			t.Fatalf("wrong first deploy input: %v", err)
		}
		deployed = true
		return nil
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"demo","repository":"demo/site","branch":"main","buildPath":".","access":"port","hostPort":8080,"automaticPush":true}`))
	r.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	a.githubPlan(w, r)
	var result operationPlan
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Blockers) != 0 || result.Workflow != "" || !strings.Contains(w.Body.String(), commit) {
		t.Fatalf("review failed: %d %s", w.Code, w.Body.String())
	}
	if err := a.runGithubSetup(r.Context(), a.operations.plans[result.ID]); err != nil || !deployed || len(paths) != 3 {
		t.Fatalf("first deploy failed: %v; GitHub calls %v", err, paths)
	}
}

func TestGithubDNSRecheckKeepsTheReviewAndRequiresItsOwner(t *testing.T) {
	a := &App{operations: newOperationManager()}
	t.Cleanup(a.operations.close)
	plan := &operationPlan{ID: "review", Kind: "github-setup", Name: "demo", access: &deployAccess{
		Domain: "192.0.2.10", HTTPS: true, Proxy: proxyOptions{PublicIP: "192.0.2.10"},
	}}
	review := httptest.NewRequest("POST", "/", nil)
	review.AddCookie(&http.Cookie{Name: sessionCookie, Value: "owner"})
	a.savePlan(httptest.NewRecorder(), review, plan)
	request := func(cookie, body string) *http.Request {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.SetPathValue("operation", plan.ID)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		return r
	}
	w := httptest.NewRecorder()
	a.executeOperation(w, request("owner", `{"name":"demo","acknowledged":true}`))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "DNS") {
		t.Fatalf("HTTPS review executed without DNS: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.checkGithubPlanDNS(w, request("another-session", ""))
	if w.Code != 409 || plan.githubDNSReady {
		t.Fatal("another session was allowed to refresh the review")
	}
	w = httptest.NewRecorder()
	a.checkGithubPlanDNS(w, request("owner", ""))
	if w.Code != 200 || !plan.githubDNSReady || a.operations.plans[plan.ID] != plan {
		t.Fatalf("DNS refresh did not unlock the existing review: %s", w.Body.String())
	}
}
