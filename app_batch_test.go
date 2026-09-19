package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCallAppBatchPostsOneBoundedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/apps/tables/batch" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer app-token" {
			t.Fatalf("authorization=%q", got)
		}
		var body struct {
			Calls []AppCall `json:"calls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Calls) != 2 || body.Calls[0].Tool != "list" || body.Calls[1].ID != "second" {
			t.Fatalf("calls=%#v", body.Calls)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"first","status":200,"result":{"jsonrpc":"2.0","id":1}},{"id":"second","status":200,"error":{"message":"no rows"}}]}`))
	}))
	defer server.Close()

	results, err := CallAppBatch(newHTTPPlatformClient(server.URL, "app-token"), "tables", []AppCall{
		{ID: "first", Tool: "list", Input: map[string]any{"limit": 2}},
		{ID: "second", Tool: "count"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].ID != "first" || results[1].Error == nil || results[1].Error.Message != "no rows" {
		t.Fatalf("results=%#v", results)
	}
}

func TestCallAppBatchProjectScopeInjectsProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Calls []AppCall `json:"calls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if got := body.Calls[0].Input["_project_id"]; got != "project-a" {
			t.Fatalf("project=%#v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	client := (&AppCtx{platform: newHTTPPlatformClient(server.URL, "token")}).WithProject("project-a").PlatformAPI()
	if _, err := CallAppBatch(client, "tables", []AppCall{{Tool: "list"}}); err != nil {
		t.Fatal(err)
	}
}

func TestCallAppResultRequestsInnerResultMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(HeaderAppCallResult); got != "inner" {
			t.Fatalf("result mode=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	var out struct {
		OK bool `json:"ok"`
	}
	if err := newHTTPPlatformClient(server.URL, "token").(*httpPlatformClient).CallAppResult("tables", "get", nil, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("out=%#v", out)
	}
}
