// Tests for decodeMCPEnvelope. Targets the exact wire shapes
// CallAppResult is meant to handle:
//
//   1. Full JSON-RPC + MCP content envelope — what apteva-server's
//      callback proxy returns today
//   2. Pre-unwrapped inner JSON — what testkit fakes pass back
//   3. error envelope — RPC-level errors must surface, never out
//
// Bug here = silent zero values for every cross-app call.

package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeMCPEnvelope_FullEnvelope(t *testing.T) {
	raw := json.RawMessage(`{
		"jsonrpc":"2.0","id":1,
		"result":{"content":[{"type":"text","text":"{\"files\":[{\"id\":42,\"name\":\"x.mkv\"}]}"}]}
	}`)
	var out struct {
		Files []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := decodeMCPEnvelope(raw, "storage", "files_list", &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Files) != 1 || out.Files[0].ID != 42 || out.Files[0].Name != "x.mkv" {
		t.Errorf("decoded shape = %+v", out)
	}
}

func TestDecodeMCPEnvelope_AlreadyUnwrapped(t *testing.T) {
	// Test fakes / future platform versions might hand callers the
	// inner JSON directly. CallAppResult must still work.
	raw := json.RawMessage(`{"folders":["a","b","c"]}`)
	var out struct {
		Folders []string `json:"folders"`
	}
	if err := decodeMCPEnvelope(raw, "storage", "files_list_folders", &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Folders) != 3 || out.Folders[0] != "a" {
		t.Errorf("decoded shape = %+v", out)
	}
}

func TestDecodeMCPEnvelope_BareMCPResult(t *testing.T) {
	raw := json.RawMessage(`{"content":[{"type":"text","text":"{\"ok\":true}"}]}`)
	var out struct {
		OK bool `json:"ok"`
	}
	if err := decodeMCPEnvelope(raw, "trading", "portfolio_get", &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK {
		t.Fatalf("decoded OK=false")
	}
}

func TestDecodeMCPEnvelope_RPCError(t *testing.T) {
	raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`)
	var out map[string]any
	err := decodeMCPEnvelope(raw, "storage", "nope", &out)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Errorf("error didn't mention RPC message: %v", err)
	}
	if !strings.Contains(err.Error(), "storage.nope") {
		t.Errorf("error didn't mention app.tool: %v", err)
	}
}

func TestDecodeMCPEnvelope_EmptyText(t *testing.T) {
	raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":""}]}}`)
	var out map[string]any
	if err := decodeMCPEnvelope(raw, "x", "y", &out); err == nil {
		t.Errorf("expected error for empty content text")
	}
}

func TestDecodeMCPEnvelope_InvalidInnerJSON(t *testing.T) {
	raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"not json"}]}}`)
	var out map[string]any
	err := decodeMCPEnvelope(raw, "x", "y", &out)
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !strings.Contains(err.Error(), "decode inner JSON") {
		t.Errorf("error message format changed: %v", err)
	}
}

func TestDecodeMCPEnvelope_NilOut(t *testing.T) {
	raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}]}}`)
	if err := decodeMCPEnvelope(raw, "x", "y", nil); err == nil {
		t.Errorf("expected error for nil out")
	}
}

func TestEnvironmentClientCallAppResult(t *testing.T) {
	var sawAuth bool
	var sawPath bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/environments/env-1/seed" && r.Method == http.MethodPost {
			sawPath = true
			sawAuth = r.Header.Get("Authorization") == "Bearer test-token"
			var body struct {
				Calls []EnvironmentSeedCall `json:"calls"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if len(body.Calls) != 1 || body.Calls[0].App != "trading" || body.Calls[0].Tool != "portfolio_step" {
				t.Fatalf("unexpected seed body: %+v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []json.RawMessage{json.RawMessage(`{"content":[{"type":"text","text":"{\"status\":\"ok\"}"}]}`)},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	api := newHTTPPlatformClient(ts.URL, "test-token")
	var out struct {
		Status string `json:"status"`
	}
	if err := api.CallEnvironmentAppResult("env-1", "trading", "portfolio_step", map[string]any{"tick": 1}, &out); err != nil {
		t.Fatalf("CallEnvironmentAppResult: %v", err)
	}
	if !sawPath {
		t.Fatalf("environment seed endpoint was not called")
	}
	if !sawAuth {
		t.Fatalf("Authorization header not forwarded")
	}
	if out.Status != "ok" {
		t.Fatalf("status=%q, want ok", out.Status)
	}
}

func TestRealtimeLifecycleRequestsCarryAgentIdentity(t *testing.T) {
	var renewed, killed bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("agent_id") != "42" {
			t.Fatalf("agent_id=%q", r.URL.Query().Get("agent_id"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/callback/threads/voice-one/audio-token":
			renewed = true
			_ = json.NewEncoder(w).Encode(RealtimeSpawnResult{Status: "renewed", ThreadID: "voice-one", AudioBridgeURL: "wss://bridge.test"})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/apps/callback/threads/voice-one":
			killed = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	api := newHTTPPlatformClient(ts.URL, "test-token")
	if _, err := api.RenewRealtimeAudioBridge(42, "voice-one"); err != nil {
		t.Fatal(err)
	}
	if err := api.KillThread(42, "voice-one"); err != nil {
		t.Fatal(err)
	}
	if !renewed || !killed {
		t.Fatalf("renewed=%t killed=%t", renewed, killed)
	}
}

func TestIntegrationWebhookLifecycleRequests(t *testing.T) {
	var ensured, verified bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization=%q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/apps/callback/integration-webhooks/ensure":
			var req IntegrationWebhookEnsureRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.ConnectionID != 57 || req.Role != "payment_processor" ||
				req.CallbackPath != "/webhooks/stripe" {
				t.Fatalf("ensure request=%#v", req)
			}
			ensured = true
			_ = json.NewEncoder(w).Encode(IntegrationWebhookStatus{
				ConnectionID: 57, Role: req.Role, Provider: "stripe", Status: "ready",
			})
		case "/api/apps/callback/integration-webhooks/verify":
			var req IntegrationWebhookVerifyRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.Payload != `{"id":"evt_1"}` || req.Signature != "stripe-signature" {
				t.Fatalf("verify request=%#v", req)
			}
			verified = true
			_ = json.NewEncoder(w).Encode(IntegrationWebhookVerifyResult{
				Provider: "stripe", Event: json.RawMessage(req.Payload),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	api := newHTTPPlatformClient(ts.URL, "test-token")
	status, err := api.EnsureIntegrationWebhook(IntegrationWebhookEnsureRequest{
		ConnectionID: 57,
		Role:         "payment_processor",
		CallbackPath: "/webhooks/stripe",
		Events:       []string{"checkout.session.completed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "ready" {
		t.Fatalf("ensure status=%#v", status)
	}
	result, err := api.VerifyIntegrationWebhook(IntegrationWebhookVerifyRequest{
		Role: "payment_processor", Payload: `{"id":"evt_1"}`, Signature: "stripe-signature",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "stripe" || string(result.Event) != `{"id":"evt_1"}` {
		t.Fatalf("verify result=%#v", result)
	}
	if !ensured || !verified {
		t.Fatalf("ensured=%t verified=%t", ensured, verified)
	}
}

func TestGetConnectionPublicConfig(t *testing.T) {
	var called bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/apps/callback/connections/57/public-config" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization=%q", r.Header.Get("Authorization"))
		}
		called = true
		_ = json.NewEncoder(w).Encode(ConnectionPublicConfig{
			ConnectionID: 57,
			Slug:         "stripe",
			Fields:       map[string]string{"publishableKey": "pk_test_browser"},
		})
	}))
	defer ts.Close()

	config, err := GetConnectionPublicConfig(newHTTPPlatformClient(ts.URL, "test-token"), 57)
	if err != nil {
		t.Fatal(err)
	}
	if !called || config.Fields["publishableKey"] != "pk_test_browser" {
		t.Fatalf("unexpected public config: %#v", config)
	}
}
