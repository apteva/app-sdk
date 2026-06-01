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
