package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEventBusClientUsesAuthenticatedScopedCallbacks(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing install authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/apps/callback/event-subscriptions":
			var sub AppEventSubscription
			if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
				t.Error(err)
			}
			if sub.ProjectID != "project-a" || sub.SourceInstallID != 7 || sub.Revision != 3 {
				t.Errorf("bad subscription %+v", sub)
			}
			w.Write([]byte(`{"ok":true}`))
		case "/api/apps/callback/event-subscriptions/sources":
			if r.URL.Query().Get("project_id") != "project-a" {
				t.Error("missing scope")
			}
			w.Write([]byte(`[{"install_id":7,"app":"signup","events":[{"name":"customer.signed_up"}]}]`))
		case "/api/app-events/internal/emit":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["event_id"] != "signup-1" || body["project_id"] != "project-a" {
				t.Error("publisher identity missing")
			}
			w.Write([]byte(`{"ok":true}`))
		case "/api/apps/callback/event-subscriptions/test":
			if r.Method != "DELETE" {
				t.Error("method")
			}
			w.Write([]byte(`{"ok":true}`))
		default:
			t.Error("unexpected path", r.URL.Path)
		}
	}))
	defer server.Close()
	api := newHTTPPlatformClient(server.URL, "secret").(EventBusClient)
	if e := api.PutAppEventSubscription(AppEventSubscription{Key: "test", ProjectID: "project-a", SourceInstallID: 7, Topic: "customer.signed_up", Revision: 3, Enabled: true}); e != nil {
		t.Fatal(e)
	}
	if out, e := api.ListAppEventSources("project-a"); e != nil || len(out) != 1 {
		t.Fatalf("sources %v %v", out, e)
	}
	if e := api.PublishAppEvent("project-a", "signup-1", "customer.signed_up", map[string]any{"id": 1}); e != nil {
		t.Fatal(e)
	}
	if e := api.DeleteAppEventSubscription("project-a", "test"); e != nil {
		t.Fatal(e)
	}
	if calls != 4 {
		t.Fatal(calls)
	}
}
func TestEventBusCapabilityRemainsOptional(t *testing.T) {
	var ctx *AppCtx
	if ctx.EventBusAPI() != nil {
		t.Fatal("nil context has capability")
	}
	if (&AppCtx{}).EventBusAPI() != nil {
		t.Fatal("empty context has capability")
	}
}
