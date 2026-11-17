package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const sdkComponentManifestFixture = `schema: apteva-app/v1
name: parent-example
version: 1.0.0
runtime:
  kind: service
  port: 8080
  image: example.invalid/parent:1.0.0
requires:
  permissions: [platform.apps.call]
components:
  executor:
    protocol: example.executor/v1
    required: true
    capabilities:
      passive_start: true
      drain: true
      routing: key
    manifest:
      schema: apteva-app/v1
      name: executor-example
      version: 1.0.0
      runtime:
        kind: source
        port: 8080
        source:
          repo: https://github.com/example/executor
          ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      provides:
        mcp_tools:
          - name: submit
            description: Submit an idempotent job
`

func sdkComponentFixture(t *testing.T) *Manifest {
	t.Helper()
	m, err := ParseManifest([]byte(sdkComponentManifestFixture))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestComponentManifestRoundTripAndLegacyCompatibility(t *testing.T) {
	m := sdkComponentFixture(t)
	if len(m.Components) != 1 || m.Components["executor"].Manifest.DisplayName != "executor-example" {
		t.Fatal("declaration/defaults missing")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var stored Manifest
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	y, err := yaml.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := ParseManifest(y)
	if err != nil {
		t.Fatal(err)
	}
	// Existing manifest fields encode nil/empty lists identically in YAML.
	// Compare the canonical declaration rather than Go's nil slice identity.
	before, _ := yaml.Marshal(m.Components)
	after, _ := yaml.Marshal(reloaded.Components)
	if string(before) != string(after) {
		t.Fatal("components lost through YAML/JSON/restart round trip", err)
	}
	legacy, err := ParseManifest([]byte(strings.Split(sdkComponentManifestFixture, "components:")[0]))
	if err != nil || len(legacy.Components) != 0 {
		t.Fatal("legacy app requires new configuration", err)
	}
	raw, _ = json.Marshal(legacy)
	if strings.Contains(string(raw), `"components"`) {
		t.Fatal("optional components not omitted for existing apps")
	}
}

func TestComponentManifestValidation(t *testing.T) {
	for _, change := range [][2]string{
		{"  executor:", "  ../executor:"},
		{"protocol: example.executor/v1", "protocol: invalid"},
		{"passive_start: true", "passive_start: false"},
		{"drain: true", "drain: false"},
		{"routing: key", "routing: guessed"},
		{"ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "ref: main"},
		{"    required: true", "    typo: true"},
		{"            description: Submit an idempotent job", "            unknown_option: true"},
	} {
		t.Run(change[1], func(t *testing.T) {
			if _, err := ParseManifest([]byte(strings.Replace(sdkComponentManifestFixture, change[0], change[1], 1))); err == nil {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
	for name, mutate := range map[string]func(*ComponentDeclaration){
		"public route": func(d *ComponentDeclaration) {
			d.Manifest.Provides.HTTPRoutes = []RouteSpec{{Prefix: "/public/", NoAuth: true}}
		},
		"UI": func(d *ComponentDeclaration) {
			d.Manifest.Provides.UIPanels = []UIPanel{{}}
		},
		"dependency inheritance": func(d *ComponentDeclaration) { d.Manifest.Requires.Apps = []RequiredAppRef{{Name: "storage"}} },
		"nested/cyclic":          func(d *ComponentDeclaration) { d.Manifest.Components = ComponentDeclarations{"nested": *d} },
		"package mismatch": func(d *ComponentDeclaration) {
			d.Manifest.Runtime.Package = &RuntimePackage{AppCapabilities: &RuntimeAppCapabilities{PassiveStart: true, Drain: true, Routing: "stateless"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := sdkComponentFixture(t)
			def := m.Components["executor"]
			mutate(&def)
			m.Components["executor"] = def
			if err := ValidateManifest(m); err == nil {
				t.Fatal("unsafe component accepted")
			}
		})
	}
	m := sdkComponentFixture(t)
	def := m.Components["executor"]
	for i := 0; i < 17; i++ {
		m.Components[fmt.Sprintf("worker-%d", i)] = def
	}
	if ValidateManifest(m) == nil {
		t.Fatal("unbounded component count")
	}
}

func TestComponentCallsUseExistingScopedTransport(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer parent-token" || r.Header.Get(HeaderAppCallDeadline) == "" {
			t.Error("lost authentication/deadline")
		}
		if r.URL.Path == "/api/apps/callback/apps/component:executor/batch" {
			var request struct {
				Calls []AppCall `json:"calls"`
				AppBatchOptions
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Calls) != 1 || request.Calls[0].Input["_project_id"] != "project-a" {
				t.Error("lost batch/project scope")
			}
			_, _ = w.Write([]byte(`{"results":[{"id":"one","status":200,"format":"json","result":{"job_id":"job-1"}}]}`))
			return
		}
		if r.URL.Path != "/api/apps/callback/apps/component:executor/call" {
			t.Errorf("unexpected component route: %s", r.URL.Path)
		}
		var request struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Tool != "submit" || request.Input["_project_id"] != "project-a" {
			t.Error("lost tool/project scope")
		}
		if r.Header.Get(HeaderAppCallResult) == "inner" {
			w.Header().Set(HeaderAppResultFormat, AppResultJSON)
			_, _ = w.Write([]byte(`{"job_id":"job-1"}`))
		} else {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"accepted":true}}`))
		}
	}))
	defer server.Close()
	app := (&AppCtx{manifest: sdkComponentFixture(t), platform: newHTTPPlatformClient(server.URL, "parent-token")}).WithProject("project-a")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input := map[string]any{"job_id": "job-1"}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := app.Components().Call(ctx, "executor", "submit", input, &out); err != nil || out.JobID != "job-1" {
		t.Fatal("component call failed", err)
	}
	raw, err := app.Components().CallRaw(ctx, "executor", "submit", input)
	if err != nil || !strings.Contains(string(raw), `"jsonrpc"`) {
		t.Fatal("raw call lost envelope", err)
	}
	results, err := app.Components().CallBatch(ctx, "executor", []AppCall{{ID: "one", Tool: "submit", Input: input}}, AppBatchOptions{ResultMode: "json"})
	if err != nil || len(results) != 1 {
		t.Fatal("batch failed", err)
	}
	if err = results[0].Decode(&out); err != nil || out.JobID != "job-1" {
		t.Fatal(err)
	}
	for _, name := range []string{"undeclared", "../executor", "component:executor", ""} {
		if err := app.Components().Call(ctx, name, "submit", nil, &out); err == nil {
			t.Fatal("undeclared/invalid target accepted")
		}
	}
	if requests.Load() != 3 {
		t.Fatal("invalid calls dispatched or duplicate requests")
	}
	if _, ok := input["_project_id"]; ok {
		t.Fatal("caller input mutated")
	}
	var absent *AppCtx
	if err := absent.Components().Call(ctx, "executor", "submit", nil, &out); err == nil {
		t.Fatal("missing API panicked or succeeded")
	}
}

func TestComponentCallCancellationAndFailureDoNotFallBack(t *testing.T) {
	started := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	app := &AppCtx{manifest: sdkComponentFixture(t), platform: newHTTPPlatformClient(server.URL, "parent-token")}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { var out any; done <- app.Components().Call(ctx, "executor", "submit", nil, &out) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation dropped", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("call ignored cancellation")
	}
	if requests.Load() != 1 {
		t.Fatal("cancellation started another execution")
	}
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "component unavailable", 503)
	}))
	defer failed.Close()
	app.platform = newHTTPPlatformClient(failed.URL, "parent-token")
	var out any
	if err := app.Components().Call(context.Background(), "executor", "submit", nil, &out); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatal("remote failure hidden", err)
	}
	if requests.Load() != 2 {
		t.Fatal("remote failure was retried implicitly")
	}
}

type componentDescriptorTestApp struct {
	metaTestApp
	protocol string
}

func (a componentDescriptorTestApp) ComponentProtocol() string { return a.protocol }
func (a *componentDescriptorTestApp) Manifest() Manifest {
	return Manifest{Schema: SchemaCurrent, Name: "component-worker", Version: "1.0.0"}
}

func TestComponentInvalidProtocolFailsBeforeStartup(t *testing.T) {
	if err := runApp(context.Background(), &componentDescriptorTestApp{protocol: "unversioned"}); err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatal("invalid worker protocol did not fail before listening", err)
	}
}

func TestComponentDescriptorOptionalAndAlwaysAuthenticated(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "worker-token")
	mux := http.NewServeMux()
	mountFrameworkRoutes(mux, &componentDescriptorTestApp{protocol: "example.executor/v1"}, &AppCtx{})
	for _, tc := range []struct {
		method, token string
		status        int
	}{{"GET", "", 401}, {"GET", "parent-token", 401}, {"GET", "worker-token", 200}, {"POST", "worker-token", 405}} {
		req := httptest.NewRequest(tc.method, ComponentDescribePath, nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		resp := httptest.NewRecorder()
		mux.ServeHTTP(resp, req)
		if resp.Code != tc.status {
			t.Fatalf("descriptor %s status %d", tc.method, resp.Code)
		}
		if resp.Code == 200 {
			var d ComponentDescriptor
			if json.Unmarshal(resp.Body.Bytes(), &d) != nil || d.Protocol != "example.executor/v1" {
				t.Fatal("descriptor mismatch")
			}
		}
	}
	t.Setenv("APTEVA_APP_TOKEN", "")
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, httptest.NewRequest("GET", ComponentDescribePath, nil))
	if resp.Code != 401 {
		t.Fatal("development mode exposed descriptor")
	}
	legacy := http.NewServeMux()
	mountFrameworkRoutes(legacy, &metaTestApp{}, &AppCtx{})
	resp = httptest.NewRecorder()
	legacy.ServeHTTP(resp, httptest.NewRequest("GET", ComponentDescribePath, nil))
	if resp.Code != 404 {
		t.Fatal("legacy app gained component routes")
	}
}
