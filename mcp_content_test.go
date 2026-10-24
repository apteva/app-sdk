package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestNativeMCPContentAndLegacyJSON(t *testing.T) {
	native := MCPResult{Content: []MCPContent{
		{Type: "text", Text: ""}, {Type: "image", MIMEType: "image/png", Data: "aGVsbG8="},
	}, StructuredContent: map[string]any{"pages": 2}, IsError: true}
	for _, tc := range []struct {
		name   string
		value  any
		native bool
	}{
		{"value", native, true}, {"pointer", &native, true},
		{"ordinary JSON", map[string]any{"content_base64": "aGVsbG8=", "name": "source.png"}, false},
		{"content-shaped JSON", map[string]any{"content": []any{map[string]any{"type": "image", "data": "aGVsbG8="}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &internalBatchTestApp{tools: []Tool{{Name: "read", Handler: func(*AppCtx, map[string]any) (any, error) { return tc.value, nil }}}}
			manifest := app.Manifest()
			h := newMCPHandler(app, &AppCtx{manifest: &manifest})
			for _, direct := range []bool{false, true} {
				req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"read"}}`))
				if direct {
					req.Header.Set(HeaderBoundCallerInstallID, "1")
					req.Header.Set(HeaderAppResultFormat, AppResultJSON)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				var wire map[string]json.RawMessage
				if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
					t.Fatal(err)
				}
				if wire["error"] != nil {
					t.Fatalf("unexpected error: %s", rec.Body.String())
				}
				result := wire["result"]
				if direct {
					if rec.Header().Get(HeaderAppResultFormat) != AppResultJSON {
						t.Fatal("direct format lost")
					}
					result = rec.Body.Bytes()
				}
				if tc.native || direct {
					want, _ := marshalJSONNoHTMLEscape(tc.value)
					var gotJSON, wantJSON any
					_ = json.Unmarshal(result, &gotJSON)
					_ = json.Unmarshal(want, &wantJSON)
					gotBytes, _ := json.Marshal(gotJSON)
					wantBytes, _ := json.Marshal(wantJSON)
					if string(gotBytes) != string(wantBytes) {
						t.Fatalf("content changed: %s", result)
					}
				} else {
					var wrapped MCPResult
					if err := json.Unmarshal(result, &wrapped); err != nil || len(wrapped.Content) != 1 || wrapped.Content[0].Type != "text" {
						t.Fatalf("legacy wrapper changed: %s", result)
					}
					var decoded any
					if err := json.Unmarshal([]byte(wrapped.Content[0].Text), &decoded); err != nil {
						t.Fatal(err)
					}
					want, _ := json.Marshal(tc.value)
					var expected any
					_ = json.Unmarshal(want, &expected)
					if !reflect.DeepEqual(decoded, expected) {
						t.Fatal("legacy JSON fields changed")
					}

				}
			}
		})
	}
}

func TestNativeMCPContentPreservesBatchAndPermissionChecks(t *testing.T) {
	app := &internalBatchTestApp{tools: []Tool{{Name: "read", HandlerCtx: func(ctx context.Context, app *AppCtx, args map[string]any) (any, error) {
		if CallerFrom(ctx) == nil || app.CurrentProject() != "project-1" {
			return nil, errors.New("caller or project lost")
		}
		return MCPResult{Content: []MCPContent{{Type: "text", Text: "document"}, {Type: "image", MIMEType: "image/png", Data: "aGVsbG8="}}}, nil
	}}}}
	manifest := app.Manifest()
	manifest.Provides.MCPTools = []MCPToolSpec{{Name: "read", Requires: "files.read"}}
	h := newMCPHandler(app, &AppCtx{manifest: &manifest})
	if _, _, err := h.callToolResult(context.Background(), &Caller{DefaultEffect: "deny"}, false, "read", nil); err == nil {
		t.Fatal("permission gate bypassed")
	}
	rec := runInternalBatch(t, newInternalAppBatchHandler(h), `{"calls":[{"tool":"read","input":{"_project_id":"project-1"}}]}`, map[string]string{HeaderBoundCallerInstallID: "1", HeaderBoundCallerAppName: "docs"})
	var batch InternalAppBatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 1 || batch.Results[0].Error != nil || batch.Results[0].Format != "json" {
		t.Fatalf("batch failed: %s", rec.Body.String())
	}
	var result MCPResult
	if err := json.Unmarshal(batch.Results[0].Result, &result); err != nil || len(result.Content) != 2 || result.Content[1].Data != "aGVsbG8=" {
		t.Fatalf("batch content lost: %s", batch.Results[0].Result)
	}
}

func TestNativeMCPContentRejectsInvalidResults(t *testing.T) {
	for _, value := range []any{(*MCPResult)(nil), MCPResult{Content: []MCPContent{{Type: "image", MIMEType: "image/png", Data: "!invalid"}}}} {
		app := &internalBatchTestApp{tools: []Tool{{Name: "read", Handler: func(*AppCtx, map[string]any) (any, error) { return value, nil }}}}
		manifest := app.Manifest()
		h := newMCPHandler(app, &AppCtx{manifest: &manifest})
		if _, _, err := h.callToolResult(context.Background(), nil, false, "read", nil); err == nil {
			t.Fatal("invalid result accepted")
		}
	}
}

func TestNativeMCPContentOlderAppCallbackPreservesEveryBlock(t *testing.T) {
	want := MCPResult{Content: []MCPContent{{Type: "text", Text: ""}, {Type: "image", MIMEType: "image/png", Data: "aGVsbG8="}}, StructuredContent: map[string]any{"next_offset": float64(4)}, IsError: true}
	bare, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	envelope, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": json.RawMessage(bare)})
	for _, raw := range []json.RawMessage{bare, envelope} {
		var got MCPResult
		if err := decodeMCPEnvelope(raw, "storage", "files_read", &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("native callback lost content: %+v", got)
		}
	}
}
