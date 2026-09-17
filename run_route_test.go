package sdk

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type frontendRouteTestApp struct{ metaTestApp }

func (*frontendRouteTestApp) Manifest() Manifest {
	return Manifest{Provides: Provides{HTTPRoutes: []RouteSpec{
		{Prefix: "/ui/frontend.json", NoAuth: true},
		{Prefix: "/ui/frontend/", NoAuth: true},
		{Prefix: "/user/", NoAuth: true},
	}}}
}

func (*frontendRouteTestApp) HTTPRoutes() []Route {
	return []Route{{Pattern: "/user/", NoAuth: true, Handler: func(w http.ResponseWriter, r *http.Request) {
		// The app retains responsibility for application-user authentication.
		if r.Header.Get("Authorization") != "Bearer application-user-session" {
			http.Error(w, "invalid user session", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}}}
}

func TestManifestPublicFrontendRoutesWithUserBearer(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "installation-secret")
	uiDir := t.TempDir()
	t.Setenv("APTEVA_UI_DIR", uiDir)
	if err := os.Mkdir(filepath.Join(uiDir, "frontend"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"frontend.json":        `{"schema":"apteva-app-frontend/v1"}`,
		"frontend/client.mjs":  "export const client = true;",
		"frontend-private.mjs": "private",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(uiDir, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	previous := publicRoutePaths
	publicRoutePaths = nil
	t.Cleanup(func() { publicRoutePaths = previous })
	app := &frontendRouteTestApp{}
	mux := http.NewServeMux()
	mountAppRoutes(mux, app, &AppCtx{})
	mountFrameworkRoutes(mux, app, &AppCtx{})
	server := httptest.NewServer(withTokenAuth(mux, app.Manifest().Provides.HTTPRoutes...))
	defer server.Close()
	for _, tc := range []struct {
		path, bearer string
		status       int
		body         string
	}{
		{"/ui/frontend.json", "", 200, files["frontend.json"]},
		{"/ui/frontend.json", "application-user-session", 200, files["frontend.json"]},
		{"/ui/frontend/client.mjs", "", 200, files["frontend/client.mjs"]},
		{"/ui/frontend/client.mjs", "application-user-session", 200, files["frontend/client.mjs"]},
		{"/ui/frontend-private.mjs", "application-user-session", 401, ""},
		{"/ui/frontend-private.mjs", "", 401, ""},
		{"/ui/frontend-private.mjs", "installation-secret", 200, "private"},
		{"/user/calls", "application-user-session", 204, ""},
		{"/user/calls", "", 401, ""},
		{"/user/calls", "invalid-session", 401, ""},
		{"/manifest", "application-user-session", 401, ""},
	} {
		req, err := http.NewRequest(http.MethodGet, server.URL+tc.path+"?project_id=project&install_id=1015", nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+tc.bearer)
		}
		// As on the platform proxy, this header never replaces the caller bearer.
		req.Header.Set("X-Apteva-App-Token", "installation-secret")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != tc.status || (tc.body != "" && string(body) != tc.body) {
			t.Errorf("%s bearer=%q: status=%d body=%q, want status=%d body=%q", tc.path, tc.bearer, res.StatusCode, body, tc.status, tc.body)
		}
	}
}

func TestManifestPublicRoutesRespectMethodAndHandlerScope(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "installation-secret")
	previous := publicRoutePaths
	publicRoutePaths = nil
	t.Cleanup(func() { publicRoutePaths = previous })
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	handler := withTokenAuth(next,
		RouteSpec{Prefix: "/ui/{file}", Method: "GET", NoAuth: true},
		RouteSpec{Prefix: "/private", NoAuth: false},
	)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/ui/frontend.json", 204},
		{"POST", "/ui/frontend.json", 401},
		{"HEAD", "/ui/frontend.json", 401},
		{"GET", "/ui/nested/private", 401},
		{"GET", "/private", 401},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.status {
			t.Errorf("%s %s: status=%d want=%d", tc.method, tc.path, rec.Code, tc.status)
		}
	}
	rec := httptest.NewRecorder()
	withTokenAuth(next).ServeHTTP(rec, httptest.NewRequest("GET", "/ui/frontend.json", nil))
	if rec.Code != 401 {
		t.Fatalf("manifest routes leaked to another handler: %d", rec.Code)
	}
}

func TestMatchesPublicRouteServeMuxParameters(t *testing.T) {
	previous := publicRoutePaths
	publicRoutePaths = []string{
		"/v1/deliveries",
		"/v1/devices/{id}/test",
	}
	t.Cleanup(func() { publicRoutePaths = previous })

	for _, path := range []string{
		"/v1/deliveries",
		"/v1/devices/device-123/test",
	} {
		if !matchesPublicRoute(path) {
			t.Errorf("public route %q did not match", path)
		}
	}
	for _, path := range []string{
		"/v1/devices",
		"/v1/devices/device-123",
		"/v1/devices/device-123/test/extra",
	} {
		if matchesPublicRoute(path) {
			t.Errorf("private route %q matched a public pattern", path)
		}
	}
}

func TestWithTokenAuthAllowsParameterizedNoAuthRoute(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "install-token")
	previous := publicRoutePaths
	publicRoutePaths = []string{
		"/v1/deliveries",
		"/v1/devices/{id}/test",
	}
	t.Cleanup(func() { publicRoutePaths = previous })

	reached := false
	handler := withTokenAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		if got := r.Header.Get("Authorization"); got != "Bearer push_relay_grant" {
			t.Errorf("Authorization = %q, want relay grant preserved", got)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/devices/device-123/test", nil)
	req.Header.Set("Authorization", "Bearer push_relay_grant")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated || !reached {
		t.Fatalf("parameterized no-auth route status=%d reached=%v", rec.Code, reached)
	}
}

func TestSignatureQueryDoesNotBypassProtectedRoute(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "secret")
	prior := publicRoutePaths
	publicRoutePaths = []string{"/signed/"}
	defer func() { publicRoutePaths = prior }()
	handler := withTokenAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, test := range []struct {
		path, token string
		status      int
	}{{"/private?sig=invalid", "", 401}, {"/private?sig=invalid", "Bearer wrong", 401}, {"/private?sig=invalid", "Bearer secret", 204}, {"/signed/file?sig=invalid", "", 204}} {
		req := httptest.NewRequest("GET", test.path, nil)
		req.Header.Set("Authorization", test.token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != test.status {
			t.Errorf("%s status=%d want=%d", test.path, rec.Code, test.status)
		}
	}
}
