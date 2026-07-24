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
	created, err := runtimes.CreateRuntime(RuntimeCreateRequest{ID: "rt-1", MCPServerIDs: []int64{17}})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != "proj-1" || created.ProjectID != "proj-1" || len(got.MCPServerIDs) != 1 || got.MCPServerIDs[0] != 17 {
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
		case "/api/apps/callback/runtimes/catalog/managed-mcps":
			_ = json.NewEncoder(w).Encode([]RuntimeCatalogManagedMCPServer{{ID: 17, Name: "customer-tools"}})
		case "/api/apps/callback/runtimes/catalog/integrations/facebook/tools":
			_ = json.NewEncoder(w).Encode([]RuntimeCatalogIntegrationTool{{Name: "pages_list", MockResponse: json.RawMessage(`{"data":[]}`)}})
		case "/api/apps/callback/runtimes/catalog/realtime-providers":
			_ = json.NewEncoder(w).Encode([]RuntimeRealtimeProvider{{Name: "openai-realtime", DefaultVoice: "marin"}})
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
	if servers, err := c.ListRuntimeCatalogManagedMCPServers("proj-1"); err != nil || len(servers) != 1 {
		t.Fatalf("managed MCPs=%#v err=%v", servers, err)
	}
	if tools, err := c.ListRuntimeCatalogIntegrationTools("facebook"); err != nil || len(tools) != 1 {
		t.Fatalf("integration tools=%#v err=%v", tools, err)
	}
	if providers, err := c.ListRuntimeRealtimeProviders("proj-1"); err != nil || len(providers) != 1 {
		t.Fatalf("realtime providers=%#v err=%v", providers, err)
	}
	for _, path := range []string{"/api/apps/callback/runtimes/catalog/apps/42/tools", "/api/apps/callback/runtimes/catalog/managed-mcps", "/api/apps/callback/runtimes/catalog/integrations", "/api/apps/callback/runtimes/catalog/integrations/facebook/tools", "/api/apps/callback/runtimes/catalog/realtime-providers"} {
		if !seen[path] {
			t.Fatalf("path not requested: %s", path)
		}
	}
}

func TestRuntimeAgentSpawnAndWaitContracts(t *testing.T) {
	var spawn RuntimeAgentSpawnRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/apps/callback/runtimes/rt-1/agents":
			if err := json.NewDecoder(r.Body).Decode(&spawn); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(RuntimeAgent{ID: 9, Alias: "main", Provider: spawn.Provider, Model: spawn.Model})
		case "/api/apps/callback/runtimes/rt-1/agents/main/wait":
			var req RuntimeAgentWaitRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(RuntimeAgentExecution{Status: "completed", Reason: "idle", Turns: req.MaxTurns, Metrics: RuntimeAgentMetrics{Model: "claude-test"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := &httpPlatformClient{baseURL: server.URL, token: "token", client: server.Client(), slowClient: server.Client()}
	agent, err := c.SpawnRuntimeAgent("rt-1", RuntimeAgentSpawnRequest{SourceAgentID: 3, Provider: "anthropic", Model: "claude-test"})
	if err != nil {
		t.Fatal(err)
	}
	if agent.Provider != "anthropic" || spawn.Model != "claude-test" {
		t.Fatalf("agent=%#v spawn=%#v", agent, spawn)
	}
	execution, err := c.WaitRuntimeAgent("rt-1", "main", RuntimeAgentWaitRequest{MaxTurns: 7})
	if err != nil {
		t.Fatal(err)
	}
	if execution.Status != "completed" || execution.Turns != 7 || execution.Metrics.Model != "claude-test" {
		t.Fatalf("execution=%#v", execution)
	}
}

func TestRuntimeManagedMCPCallContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/apps/callback/runtimes/rt-1/managed-mcps/customer-tools/tools" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]RuntimeMCPTool{{Name: "lookup"}})
		case r.URL.Path == "/api/apps/callback/runtimes/rt-1/managed-mcps/customer-tools/call" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": `{"ok":true}`}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c := &httpPlatformClient{baseURL: server.URL, token: "token", client: server.Client(), slowClient: server.Client()}
	if tools, err := c.ListRuntimeManagedMCPTools("rt-1", "customer-tools"); err != nil || len(tools) != 1 {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := c.CallRuntimeManagedMCPResult("rt-1", "customer-tools", "lookup", map[string]any{}, &result); err != nil || !result.OK {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestRuntimeRealtimeThreadContracts(t *testing.T) {
	var spawn RuntimeRealtimeSpawnRequest
	seenRenew, seenStop := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/apps/callback/runtimes/rt-1/agents/main/realtime" && r.Method == http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&spawn); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(RealtimeSpawnResult{Status: "created", ThreadID: spawn.ThreadID, AudioBridgeURL: "ws://bridge.test"})
		case r.URL.Path == "/api/apps/callback/runtimes/rt-1/agents/main/realtime/voice/audio-token" && r.Method == http.MethodPost:
			seenRenew = true
			_ = json.NewEncoder(w).Encode(RealtimeSpawnResult{Status: "renewed", ThreadID: "voice", AudioBridgeURL: "ws://bridge.test/renewed"})
		case r.URL.Path == "/api/apps/callback/runtimes/rt-1/agents/main/realtime/voice" && r.Method == http.MethodDelete:
			seenStop = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c := &httpPlatformClient{baseURL: server.URL, token: "token", client: server.Client(), slowClient: server.Client()}
	result, err := c.SpawnRuntimeRealtimeThread("rt-1", "main", RuntimeRealtimeSpawnRequest{ThreadID: "voice", Directive: "Answer callers", Provider: "openai-realtime"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ThreadID != "voice" || spawn.Provider != "openai-realtime" {
		t.Fatalf("result=%#v spawn=%#v", result, spawn)
	}
	if _, err := c.RenewRuntimeRealtimeAudioBridge("rt-1", "main", "voice"); err != nil {
		t.Fatal(err)
	}
	if err := c.StopRuntimeRealtimeThread("rt-1", "main", "voice"); err != nil {
		t.Fatal(err)
	}
	if !seenRenew || !seenStop {
		t.Fatalf("renew=%v stop=%v", seenRenew, seenStop)
	}
}
