package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type profileTestApp struct{ calls []string }

func (a *profileTestApp) Manifest() Manifest {
	return Manifest{Name: "profile-test", Version: "0.0.0", Provides: Provides{
		MCPTools: []MCPToolSpec{
			{Name: "reply", Meta: map[string]any{"io.apteva/wakeOnResult": true}},
			{Name: "status"},
		},
		MCPProfiles: []MCPProfileSpec{
			{Name: "conversation", Tools: []string{"reply"}},
			{Name: "agent-output", Tools: []string{"status"}},
		},
	}}
}
func (*profileTestApp) OnMount(*AppCtx) error         { return nil }
func (*profileTestApp) OnUnmount(*AppCtx) error       { return nil }
func (*profileTestApp) HTTPRoutes() []Route           { return nil }
func (*profileTestApp) Channels() []ChannelFactory    { return nil }
func (*profileTestApp) Workers() []Worker             { return nil }
func (*profileTestApp) EventHandlers() []EventHandler { return nil }
func (a *profileTestApp) MCPTools() []Tool {
	handler := func(name string) ToolHandler {
		return func(*AppCtx, map[string]any) (any, error) {
			a.calls = append(a.calls, name)
			return map[string]any{"ok": true}, nil
		}
	}
	return []Tool{{Name: "reply", Handler: handler("reply")}, {Name: "status", Handler: handler("status")}}
}

func profileRPC(t *testing.T, h http.Handler, profile, method, tool string) map[string]any {
	t.Helper()
	params := `{}`
	if tool != "" {
		params = `{"name":"` + tool + `","arguments":{}}`
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
	req.Header.Set(HeaderMCPProfile, profile)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return out
}

func TestMCPProfileFiltersListAndCallsAndMergesManifestMeta(t *testing.T) {
	app := &profileTestApp{}
	manifest := app.Manifest()
	h := newMCPHandler(app, &AppCtx{manifest: &manifest})
	listed := profileRPC(t, h, "conversation", "tools/list", "")
	tools := listed["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "reply" {
		t.Fatalf("profile list=%#v", tools)
	}
	meta := tools[0].(map[string]any)["_meta"].(map[string]any)
	if meta["io.apteva/wakeOnResult"] != true {
		t.Fatalf("manifest meta=%#v", meta)
	}
	denied := profileRPC(t, h, "conversation", "tools/call", "status")
	if denied["error"] == nil || len(app.calls) != 0 {
		t.Fatalf("cross-profile call allowed: %#v calls=%v", denied, app.calls)
	}
	allowed := profileRPC(t, h, "conversation", "tools/call", "reply")
	if allowed["error"] != nil || len(app.calls) != 1 || app.calls[0] != "reply" {
		t.Fatalf("profile call=%#v calls=%v", allowed, app.calls)
	}
}

func TestManifestRejectsInvalidMCPProfiles(t *testing.T) {
	manifest := Manifest{Schema: SchemaCurrent, Name: "bad", Version: "1.0.0", Provides: Provides{
		MCPTools:    []MCPToolSpec{{Name: "public"}, {Name: "private", Exposure: ToolExposureAppOnly}},
		MCPProfiles: []MCPProfileSpec{{Name: "conversation", Tools: []string{"private"}}},
	}}
	if err := ValidateManifest(&manifest); err == nil || !strings.Contains(err.Error(), "app_only") {
		t.Fatalf("ValidateManifest error=%v", err)
	}
}
