package sdk

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
