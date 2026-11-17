package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type annotationsTestApp struct{ metaTestApp }

func (*annotationsTestApp) MCPTools() []Tool {
	return []Tool{
		{Name: "reply", Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false}},
		{Name: "writer", Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": true}},
		{Name: "status"},
		{Name: "empty", Annotations: map[string]any{}},
		{Name: "private", Exposure: ToolExposureAppOnly, Annotations: map[string]any{"readOnlyHint": true}},
	}
}

func TestMCPToolAnnotationsAreExposedWithoutChangingLegacyTools(t *testing.T) {
	app := &annotationsTestApp{}
	manifest := app.Manifest()
	h := newMCPHandler(app, &AppCtx{manifest: &manifest})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	listed := map[string]map[string]any{}
	for _, tool := range out.Result.Tools {
		listed[tool["name"].(string)] = tool
	}
	if len(listed) != 4 || listed["private"] != nil {
		t.Fatalf("annotations changed exposure: %#v", listed)
	}
	for _, tool := range app.MCPTools() {
		if tool.Exposure == ToolExposureAppOnly {
			continue
		}
		got, present := listed[tool.Name]["annotations"]
		if len(tool.Annotations) == 0 {
			if present {
				t.Errorf("legacy tool %q unexpectedly has annotations: %#v", tool.Name, got)
			}
		} else if !reflect.DeepEqual(got, tool.Annotations) {
			t.Errorf("tool %q annotations=%#v, want %#v", tool.Name, got, tool.Annotations)
		}
	}
	meta := listed["reply"]["_meta"].(map[string]any)
	if meta["io.apteva/wakeOnResult"] != true {
		t.Fatalf("annotations displaced manifest metadata: %#v", meta)
	}
	if _, nested := meta["readOnlyHint"]; nested {
		t.Fatal("standard annotations must not be nested in _meta")
	}
}
