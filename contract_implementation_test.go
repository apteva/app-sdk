package sdk

import (
	"context"
	"encoding/json"
	"github.com/apteva/app-sdk/contract"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

type contractTestApp struct {
	metaTestApp
	manifest Manifest
}

func (a *contractTestApp) Manifest() Manifest { return a.manifest }
func (a *contractTestApp) MCPTools() []Tool {
	return []Tool{{Name: "hello", Description: "A safe greeting for contract tests", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []string{"name"}}, Handler: func(ctx *AppCtx, args map[string]any) (any, error) {
		return map[string]any{"message": "Hello, Ada!", "install_id": "101", "project_id": "test"}, nil
	}}}
}
func TestGoSDKBasicAppContract(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-token")
	raw, e := os.ReadFile("rust/examples/hello/apteva.yaml")
	if e != nil {
		t.Fatal(e)
	}
	m, e := ParseManifest(raw)
	if e != nil {
		t.Fatal(e)
	}
	app := &contractTestApp{manifest: *m}
	ctx := &AppCtx{manifest: m}
	mux := http.NewServeMux()
	mountFrameworkRoutes(mux, app, ctx)
	s := httptest.NewServer(withTokenAuth(mux))
	defer s.Close()
	fixtures := contract.BasicFixtures()
	b, e := os.ReadFile("contract/testdata/hello.json")
	if e != nil {
		t.Fatal(e)
	}
	var extra []contract.Fixture
	if e = json.Unmarshal(b, &extra); e != nil {
		t.Fatal(e)
	}
	fixtures = append(fixtures, extra...)
	if e = contract.Check(context.Background(), s.URL, "test-token", fixtures); e != nil {
		t.Fatal(e)
	}
}
