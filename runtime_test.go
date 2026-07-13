package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimeClientCreateUsesAppCallbackAndProjectScope(t *testing.T) {
	var got RuntimeCreateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/runtimes" || r.Method != http.MethodPost {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer app-token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RuntimeSummary{ID: "rt-1", ProjectID: got.ProjectID, Status: "running"})
	}))
	defer server.Close()

	ctx := (&AppCtx{platform: newHTTPPlatformClient(server.URL, "app-token")}).WithProject("proj-1")
	runtimes := ctx.RuntimeAPI()
	if runtimes == nil {
		t.Fatal("default AppCtx did not expose RuntimeAPI")
	}
	created, err := runtimes.CreateRuntime(RuntimeCreateRequest{ID: "rt-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != "proj-1" || created.ProjectID != "proj-1" {
		t.Fatalf("project scope not forwarded: request=%q response=%q", got.ProjectID, created.ProjectID)
	}
}

func TestRuntimeAPIUnavailableForPlatformOnlyStub(t *testing.T) {
	ctx := (&AppCtx{platform: &stubProjectPlatformClient{}}).WithProject("proj-1")
	if ctx.RuntimeAPI() != nil {
		t.Fatal("PlatformClient-only test stub unexpectedly exposed RuntimeAPI")
	}
}

func TestRuntimeCatalogDiscoveryPaths(t *testing.T) {
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = true
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/apps/callback/runtimes/catalog/apps/42/tools":
			_ = json.NewEncoder(w).Encode([]RuntimeMCPTool{{Name: "contact_create"}})
		case "/api/apps/callback/runtimes/catalog/integrations":
			_ = json.NewEncoder(w).Encode([]RuntimeCatalogIntegration{{Slug: "facebook", Name: "Facebook"}})
		case "/api/apps/callback/runtimes/catalog/integrations/facebook/tools":
			_ = json.NewEncoder(w).Encode([]RuntimeCatalogIntegrationTool{{Name: "pages_list", MockResponse: json.RawMessage(`{"data":[]}`)}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := &httpPlatformClient{baseURL: server.URL, token: "token", client: server.Client(), slowClient: server.Client()}
	if tools, err := c.ListRuntimeCatalogAppTools(42); err != nil || len(tools) != 1 {
		t.Fatalf("app tools=%#v err=%v", tools, err)
	}
	if apps, err := c.ListRuntimeCatalogIntegrations(); err != nil || len(apps) != 1 {
		t.Fatalf("integrations=%#v err=%v", apps, err)
	}
	if tools, err := c.ListRuntimeCatalogIntegrationTools("facebook"); err != nil || len(tools) != 1 {
		t.Fatalf("integration tools=%#v err=%v", tools, err)
	}
	for _, path := range []string{"/api/apps/callback/runtimes/catalog/apps/42/tools", "/api/apps/callback/runtimes/catalog/integrations", "/api/apps/callback/runtimes/catalog/integrations/facebook/tools"} {
		if !seen[path] {
			t.Fatalf("path not requested: %s", path)
		}
	}
}
