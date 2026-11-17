package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAppInstallationSelectionAcrossScopedSingleAndBatch(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "bad", 400)
			return
		}
		if body["target_install_id"] != float64(42) {
			t.Errorf("target not preserved: %v", body)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		if body["calls"] != nil {
			w.Write([]byte(`{"results":[{"id":"read","status":200,"format":"json","result":{"ok":true}}]}`))
		} else {
			w.Header().Set(HeaderAppResultFormat, AppResultJSON)
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer server.Close()
	client := newHTTPPlatformClient(server.URL, "fixture-token")
	ctx := WithAppInstallation(context.Background(), 42)
	var out map[string]any
	if err := CallAppResultContext(ctx, client, "database", "db_get", nil, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := CallAppBatchContext(ctx, client, "database", []AppCall{{ID: "read", Tool: "db_get"}}, AppBatchOptions{}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
	if err := CallAppResultContext(WithAppInstallation(context.Background(), -1), client, "database", "db_get", nil, &out); err == nil {
		t.Fatal("negative selection accepted")
	}
}
