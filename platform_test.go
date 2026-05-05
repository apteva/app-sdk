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
