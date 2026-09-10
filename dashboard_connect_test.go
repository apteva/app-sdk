package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestDashboardConnectHTTPContract(t *testing.T) {
	t.Setenv("APTEVA_INSTALL_ID", "42")
	var saved DashboardConnectOriginRegistration
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer app-token" || r.Header.Get("X-Apteva-App-Install-ID") != "42" {
			t.Error("missing install authentication")
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPut:
			if err := json.NewDecoder(r.Body).Decode(&saved); err != nil {
				t.Error(err)
			}
			if saved.Origins == nil {
				t.Error("empty origins must be []")
			}
			saved.Key = "storage-backend"
			json.NewEncoder(w).Encode(saved)
		case http.MethodGet:
			if r.URL.Path == "/api/apps/callback/dashboard-connect-origins" {
				json.NewEncoder(w).Encode(map[string]any{"registrations": []DashboardConnectOriginRegistration{saved}})
			} else {
				json.NewEncoder(w).Encode(saved)
			}
		case http.MethodDelete:
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	ctx := (&AppCtx{platform: newHTTPPlatformClient(server.URL, "app-token")}).WithProject("project-a")
	api := ctx.DashboardConnectAPI()
	if api == nil {
		t.Fatal("missing optional client")
	}
	out, err := api.ReplaceDashboardConnectOrigins("storage-backend", []string{"https://bucket.example"})
	if err != nil || len(out.Origins) != 1 {
		t.Fatalf("replace=%+v %v", out, err)
	}
	got, err := api.GetDashboardConnectOrigins("storage-backend")
	if err != nil || !reflect.DeepEqual(out, got) {
		t.Fatalf("get=%+v %v", got, err)
	}
	list, err := api.ListDashboardConnectOriginRegistrations()
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%+v %v", list, err)
	}
	if _, err := api.ReplaceDashboardConnectOrigins("storage-backend", nil); err != nil {
		t.Fatal(err)
	}
	if err := api.DeleteDashboardConnectOrigins("storage-backend"); err != nil {
		t.Fatal(err)
	}
	want := []string{"PUT /api/apps/callback/dashboard-connect-origins/storage-backend", "GET /api/apps/callback/dashboard-connect-origins/storage-backend", "GET /api/apps/callback/dashboard-connect-origins", "PUT /api/apps/callback/dashboard-connect-origins/storage-backend", "DELETE /api/apps/callback/dashboard-connect-origins/storage-backend"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v", calls)
	}
}

func TestDashboardConnectOptionalAndUnsupported(t *testing.T) {
	var ctx *AppCtx
	if ctx.DashboardConnectAPI() != nil || (&AppCtx{}).DashboardConnectAPI() != nil {
		t.Fatal("nil context should have no API")
	}
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	api := (&AppCtx{platform: newHTTPPlatformClient(server.URL, "app-token")}).DashboardConnectAPI()
	if _, err := api.ReplaceDashboardConnectOrigins("storage", []string{"https://bucket.example"}); err == nil {
		t.Fatal("unsupported server must return an error")
	}
	if _, err := api.ReplaceDashboardConnectOrigins("a/b", nil); err == nil {
		t.Fatal("invalid key accepted")
	}
	found := false
	for _, p := range AllPermissions() {
		if p == PermDashboardConnect {
			found = true
		}
	}
	if !found || PermissionDescription(PermDashboardConnect) == string(PermDashboardConnect) {
		t.Fatal("missing consent description")
	}
}
