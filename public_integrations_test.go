package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExecutePublicIntegrationToolPostsBySlug(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/apps/callback/integrations/public/gdelt/execute" {
			t.Errorf("request = %s %q", r.Method, r.URL.Path)
		}
		var body struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Tool != "search_articles" || body.Input["query"] != "fuel" {
			t.Errorf("body = %+v", body)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"articles":[]},"status":200}`))
	}))
	defer server.Close()
	// The project wrapper must pass the optional interface through.
	client := wrapPlatformWithProject(newHTTPPlatformClient(server.URL, "token"), "project-a")
	result, err := ExecutePublicIntegrationTool(context.Background(), client, "gdelt", "search_articles", map[string]any{"query": "fuel"})
	if err != nil || result == nil || !result.Success || result.Status != 200 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestExecutePublicIntegrationToolUnavailableOnPlainClient(t *testing.T) {
	var client PlatformClient // a stub that lacks the optional interface
	if _, err := ExecutePublicIntegrationTool(context.Background(), client, "gdelt", "x", nil); !errors.Is(err, ErrPublicIntegrationsUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if _, err := ExecutePublicIntegrationTool(context.Background(), newHTTPPlatformClient("http://127.0.0.1:1", "t"), " ", "x", nil); err == nil || errors.Is(err, ErrPublicIntegrationsUnavailable) {
		t.Fatal("empty slug accepted")
	}
}
