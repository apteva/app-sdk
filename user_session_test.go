package sdk

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithUserSessionScopesCallbacksWithoutMutatingMountedContext(t *testing.T) {
	var received []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = append(received, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	mounted := &AppCtx{platform: newHTTPPlatformClient(srv.URL, "app-token")}
	req := httptest.NewRequest("GET", "/ui", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "user-session"})
	req.AddCookie(&http.Cookie{Name: "unrelated", Value: "private"})
	scoped := mounted.WithProject("project-a").WithUserSession(req)
	if scoped.CurrentProject() != "project-a" {
		t.Fatal("lost project scope")
	}
	for _, ctx := range []*AppCtx{scoped, mounted} {
		if _, err := ctx.PlatformAPI().WhoAmI(); err != nil {
			t.Fatal(err)
		}
	}
	if len(received) != 3 {
		t.Fatalf("requests=%d", len(received))
	}
	if received[0].Get("X-Apteva-User-Session") != "user-session" || received[0].Get("Authorization") != "Bearer app-token" {
		t.Fatal("request did not retain both credentials")
	}
	if received[2].Get("X-Apteva-User-Session") != "" {
		t.Fatal("session leaked into mounted context")
	}
	if received[0].Get("Cookie") != "" {
		t.Fatal("forwarded unrelated cookies")
	}
	client := scoped.platform.(*projectScopedClient).inner.(*httpPlatformClient)
	outside := httptest.NewRequest("GET", srv.URL+"/api/apps/other/tools", nil)
	client.addAuth(outside)
	if outside.Header.Get("X-Apteva-User-Session") != "" {
		t.Fatal("session leaked outside callback surface")
	}
	if mounted.WithUserSession(httptest.NewRequest("GET", "/", nil)) != mounted {
		t.Fatal("missing session should preserve service context")
	}
	var absent *AppCtx
	if absent.WithUserSession(req) != nil {
		t.Fatal("nil receiver")
	}
}
