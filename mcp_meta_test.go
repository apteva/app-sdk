package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type metaTestApp struct{}

func (*metaTestApp) Manifest() Manifest {
	return Manifest{Name: "meta-test", Version: "0.0.0", Provides: Provides{
		MCPTools: []MCPToolSpec{
			{Name: "reply", Meta: map[string]any{"io.apteva/wakeOnResult": true}},
			{Name: "status"},
		},
	}}
}
func (*metaTestApp) OnMount(*AppCtx) error         { return nil }
func (*metaTestApp) OnUnmount(*AppCtx) error       { return nil }
func (*metaTestApp) HTTPRoutes() []Route           { return nil }
func (*metaTestApp) Channels() []ChannelFactory    { return nil }
func (*metaTestApp) Workers() []Worker             { return nil }
func (*metaTestApp) EventHandlers() []EventHandler { return nil }
func (*metaTestApp) MCPTools() []Tool {
	handler := func(*AppCtx, map[string]any) (any, error) { return map[string]any{"ok": true}, nil }
	return []Tool{{Name: "reply", Handler: handler}, {Name: "status", Handler: handler}}
}

func TestManifestToolMetaIsExposedAtRuntime(t *testing.T) {
	app := &metaTestApp{}
	manifest := app.Manifest()
	h := newMCPHandler(app, &AppCtx{manifest: &manifest})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	tools := out["result"].(map[string]any)["tools"].([]any)
	meta := tools[0].(map[string]any)["_meta"].(map[string]any)
	if meta["io.apteva/wakeOnResult"] != true {
		t.Fatalf("manifest meta=%#v", meta)
	}
}

func TestLegacyMCPProfileHeaderDoesNotSplitToolSurface(t *testing.T) {
	app := &metaTestApp{}
	manifest := app.Manifest()
	h := newMCPHandler(app, &AppCtx{manifest: &manifest})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("X-Apteva-MCP-Profile", "conversation")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	tools := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("legacy profile header split the tool surface: %#v", tools)
	}
}
