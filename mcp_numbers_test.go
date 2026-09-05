package sdk

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

type numberPolicyApp struct {
	metaTestApp
	preserve bool
}

func (a *numberPolicyApp) PreserveJSONNumbers() bool { return a.preserve }
func (a *numberPolicyApp) MCPTools() []Tool {
	return []Tool{{Name: "reply", Handler: func(_ *AppCtx, args map[string]any) (any, error) {
		return map[string]any{"args": args, "number_type": fmt.Sprintf("%T", args["n"])}, nil
	}}}
}
func TestMCPNumbersOptInPreservesNestedNumbersAndRPCID(t *testing.T) {
	app := &numberPolicyApp{preserve: true}
	manifest := app.Manifest()
	handler := newMCPHandler(app, &AppCtx{manifest: &manifest})
	request := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"name":"reply","arguments":{"n":9007199254740993,"nested":{"decimal":0.1234567890123456789},"array":[9007199254740995]}}}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	raw := rec.Body.String()
	for _, want := range []string{`"id":9007199254740993`, `9007199254740993`, `9007199254740995`, `0.1234567890123456789`, `json.Number`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("lost %s: %s", want, raw)
		}
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatal(raw)
	}
}
func TestMCPNumbersDefaultRemainsFloat64(t *testing.T) {
	app := &numberPolicyApp{}
	manifest := app.Manifest()
	handler := newMCPHandler(app, &AppCtx{manifest: &manifest})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"reply","arguments":{"n":42}}}`)))
	if !strings.Contains(rec.Body.String(), "float64") {
		t.Fatal(rec.Body.String())
	}
}
