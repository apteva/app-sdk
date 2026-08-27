package sdk

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestAgentToolsHTTPContract(t *testing.T) {
	var got EnsureAppToolsRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/apps/callback/agent-tools/ensure-attached" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer app-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(EnsureAppToolsResult{
			AgentID: 7, AttachedInstallIDs: []int64{11, 12}, MCPServerIDs: []int64{21, 22},
			Changed: true, Applied: true, AgentRunning: true, ResetThreads: 2,
		})
	}))
	defer server.Close()

	ctx := &AppCtx{platform: newHTTPPlatformClient(server.URL, "app-token")}
	api := ctx.AgentToolsAPI()
	if api == nil {
		t.Fatal("default AppCtx did not expose AgentToolsAPI")
	}
	result, err := api.EnsureAppToolsAttached(EnsureAppToolsRequest{
		AgentKind: AgentKindPlatformHelper, IncludeRequiredApps: []string{"conversations"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentKind != AgentKindPlatformHelper || !reflect.DeepEqual(got.IncludeRequiredApps, []string{"conversations"}) {
		t.Fatalf("request = %#v", got)
	}
	if result.AgentID != 7 || !result.Changed || !result.Applied || result.ResetThreads != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestAgentToolsTypedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "target_agent_not_found", "error": "Helper is not activated", "agent_kind": "platform_helper",
		})
	}))
	defer server.Close()

	api := newHTTPPlatformClient(server.URL, "app-token").(AgentToolsClient)
	_, err := api.EnsureAppToolsAttached(EnsureAppToolsRequest{AgentKind: AgentKindPlatformHelper})
	if !IsAgentToolsErrorCode(err, "target_agent_not_found") {
		t.Fatalf("error = %#v", err)
	}
	var typed *AgentToolsError
	if !errors.As(err, &typed) || typed.StatusCode != http.StatusConflict || typed.AgentKind != AgentKindPlatformHelper {
		t.Fatalf("typed error = %#v", typed)
	}
}

func TestAgentToolsValidationAndOptionalCapability(t *testing.T) {
	for _, req := range []EnsureAppToolsRequest{
		{},
		{AgentID: 1, AgentKind: AgentKindPlatformHelper},
		{AgentKind: "arbitrary"},
		{AgentID: 1, IncludeRequiredApps: []string{"conversations", "conversations"}},
	} {
		if err := validateEnsureAppToolsRequest(req); err == nil {
			t.Fatalf("request unexpectedly valid: %#v", req)
		}
	}
	plain := (&AppCtx{platform: &stubProjectPlatformClient{}}).WithProject("project-a")
	if plain.AgentToolsAPI() != nil {
		t.Fatal("plain PlatformClient unexpectedly implements AgentToolsClient")
	}
	if got := PermissionDescription(PermMCPAttach); got == string(PermMCPAttach) {
		t.Fatal("platform.mcp.attach is missing install-consent copy")
	}
}
