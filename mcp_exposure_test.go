package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type exposureTestApp struct {
	privateCalls int
}

func (a *exposureTestApp) Manifest() Manifest {
	return Manifest{
		Name: "exposure-test", Version: "0.0.0",
		Provides: Provides{MCPTools: []MCPToolSpec{
			{Name: "public_tool"},
			{Name: "private_tool", Exposure: ToolExposureAppOnly},
		}},
	}
}
func (*exposureTestApp) OnMount(*AppCtx) error         { return nil }
func (*exposureTestApp) OnUnmount(*AppCtx) error       { return nil }
func (*exposureTestApp) HTTPRoutes() []Route           { return nil }
func (*exposureTestApp) Channels() []ChannelFactory    { return nil }
func (*exposureTestApp) Workers() []Worker             { return nil }
func (*exposureTestApp) EventHandlers() []EventHandler { return nil }
func (a *exposureTestApp) MCPTools() []Tool {
	return []Tool{
		{Name: "public_tool", Handler: func(*AppCtx, map[string]any) (any, error) { return map[string]any{"ok": true}, nil }},
		{Name: "private_tool", Exposure: ToolExposureAppOnly, Handler: func(*AppCtx, map[string]any) (any, error) {
			a.privateCalls++
			return map[string]any{"private": true}, nil
		}},
	}
}

func exposureRPC(t *testing.T, h http.Handler, method, tool, callerInstall string) map[string]any {
	t.Helper()
	params := `{}`
	if tool != "" {
		params = `{"name":"` + tool + `","arguments":{}}`
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
	if callerInstall != "" {
		req.Header.Set(HeaderBoundCallerInstallID, callerInstall)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode MCP response: %v; body=%s", err, rec.Body.String())
	}
	return out
}

func TestAppOnlyToolIsHiddenAndRequiresBoundAppCaller(t *testing.T) {
	app := &exposureTestApp{}
	manifest := app.Manifest()
	h := newMCPHandler(app, &AppCtx{manifest: &manifest})

	listed := exposureRPC(t, h, "tools/list", "", "")
	result := listed["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "public_tool" {
		t.Fatalf("tools/list exposed private tool: %#v", tools)
	}

	denied := exposureRPC(t, h, "tools/call", "private_tool", "")
	if denied["error"] == nil || app.privateCalls != 0 {
		t.Fatalf("untrusted private call was not denied: response=%#v calls=%d", denied, app.privateCalls)
	}

	allowed := exposureRPC(t, h, "tools/call", "private_tool", "42")
	if allowed["error"] != nil || app.privateCalls != 1 {
		t.Fatalf("bound app private call failed: response=%#v calls=%d", allowed, app.privateCalls)
	}
}
