package sdk

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type escapeTestApp struct{}

func (escapeTestApp) Manifest() Manifest {
	return Manifest{Name: "escape-test", Version: "0.0.0"}
}
func (escapeTestApp) OnMount(*AppCtx) error         { return nil }
func (escapeTestApp) OnUnmount(*AppCtx) error       { return nil }
func (escapeTestApp) HTTPRoutes() []Route           { return nil }
func (escapeTestApp) Channels() []ChannelFactory    { return nil }
func (escapeTestApp) Workers() []Worker             { return nil }
func (escapeTestApp) EventHandlers() []EventHandler { return nil }
func (escapeTestApp) MCPTools() []Tool {
	return []Tool{{
		Name: "signed_url",
		Handler: func(*AppCtx, map[string]any) (any, error) {
			return map[string]any{
				"url": "https://storage.example.com/file.mp4?X-Amz-Date=20260626T120000Z&X-Amz-Expires=86400&response-content-type=video%2Fmp4",
			}, nil
		},
	}}
}

func TestMCPToolsCallDoesNotHTMLEscapeSignedURLSeparators(t *testing.T) {
	app := escapeTestApp{}
	handler := newMCPHandler(app, &AppCtx{manifest: ptrManifest(app.Manifest())})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":1,
		"method":"tools/call",
		"params":{"name":"signed_url","arguments":{}}
	}`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, `\u0026`) {
		t.Fatalf("raw MCP response HTML-escaped URL separators: %s", body)
	}
	if !strings.Contains(body, `X-Amz-Date=20260626T120000Z&X-Amz-Expires=86400&response-content-type=video%2Fmp4`) {
		t.Fatalf("raw MCP response lost plain query separators: %s", body)
	}

	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode outer response: %v", err)
	}
	if len(env.Result.Content) != 1 {
		t.Fatalf("content count=%d, want 1", len(env.Result.Content))
	}
	var inner struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(env.Result.Content[0].Text), &inner); err != nil {
		t.Fatalf("decode inner response: %v", err)
	}
	if !strings.Contains(inner.URL, "&X-Amz-Expires=86400&") {
		t.Fatalf("decoded URL separators changed: %q", inner.URL)
	}
}

func ptrManifest(m Manifest) *Manifest {
	return &m
}
