package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestAppCtxBrowserOriginsHTTPContract(t *testing.T) {
	t.Setenv("APTEVA_INSTALL_ID", "42")
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer app-token" {
			t.Errorf("Authorization=%q", got)
		}
		if got := r.Header.Get("X-Apteva-App-Install-ID"); got != "42" {
			t.Errorf("install header=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/apps/callback/cors-origins/oauth-client-1":
			var body struct {
				Origins []string `json:"origins"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode PUT: %v", err)
			}
			if !reflect.DeepEqual(body.Origins, []string{"https://app.example", "http://localhost:3000"}) {
				t.Errorf("origins=%v", body.Origins)
			}
			_, _ = w.Write([]byte(`{"key":"oauth-client-1","origins":["https://app.example","http://localhost:3000"]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/callback/cors-origins":
			_, _ = w.Write([]byte(`{"registrations":[{"key":"oauth-client-1","origins":["https://app.example","http://localhost:3000"]}]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/apps/callback/cors-origins/oauth-client-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	ctx := (&AppCtx{platform: newHTTPPlatformClient(server.URL, "app-token")}).WithProject("project-a")
	saved, err := ctx.ReplaceBrowserOrigins("oauth-client-1", []string{"https://app.example", "http://localhost:3000"})
	if err != nil {
		t.Fatalf("ReplaceBrowserOrigins: %v", err)
	}
	if saved.Key != "oauth-client-1" || len(saved.Origins) != 2 {
		t.Fatalf("saved=%+v", saved)
	}
	if saved.Preflight != BrowserPreflightPlatform || !saved.Credentials {
		t.Fatalf("legacy response defaults=%+v", saved)
	}
	registrations, err := ctx.ListBrowserOriginRegistrations()
	if err != nil {
		t.Fatalf("ListBrowserOriginRegistrations: %v", err)
	}
	if len(registrations) != 1 || registrations[0].Key != "oauth-client-1" {
		t.Fatalf("registrations=%+v", registrations)
	}
	if registrations[0].Preflight != BrowserPreflightPlatform || !registrations[0].Credentials {
		t.Fatalf("legacy list response defaults=%+v", registrations[0])
	}
	if err := ctx.DeleteBrowserOrigins("oauth-client-1"); err != nil {
		t.Fatalf("DeleteBrowserOrigins: %v", err)
	}
	wantCalls := []string{
		"PUT /api/apps/callback/cors-origins/oauth-client-1",
		"GET /api/apps/callback/cors-origins",
		"DELETE /api/apps/callback/cors-origins/oauth-client-1",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls=%v want=%v", calls, wantCalls)
	}
}

func TestAppCtxBrowserOriginPolicyHTTPContract(t *testing.T) {
	t.Setenv("APTEVA_INSTALL_ID", "42")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer app-token" {
			t.Errorf("Authorization=%q", got)
		}
		if got := r.Header.Get("X-Apteva-App-Install-ID"); got != "42" {
			t.Errorf("install header=%q", got)
		}
		var policy BrowserOriginPolicy
		if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
			t.Fatalf("decode PUT: %v", err)
		}
		if !reflect.DeepEqual(policy.Origins, []string{"https://api.example"}) ||
			policy.Preflight != BrowserPreflightApp || policy.Credentials {
			t.Errorf("policy=%+v", policy)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"api-1","origins":["https://api.example"],"preflight":"app","credentials":false}`))
	}))
	defer server.Close()

	ctx := (&AppCtx{platform: newHTTPPlatformClient(server.URL, "app-token")}).WithProject("project-a")
	saved, err := ctx.ReplaceBrowserOriginPolicy("api-1", BrowserOriginPolicy{
		Origins:     []string{"https://api.example"},
		Preflight:   BrowserPreflightApp,
		Credentials: false,
	})
	if err != nil {
		t.Fatalf("ReplaceBrowserOriginPolicy: %v", err)
	}
	if saved.Preflight != BrowserPreflightApp || saved.Credentials {
		t.Fatalf("saved=%+v", saved)
	}
}

func TestReplaceBrowserOriginPolicyUsesExplicitEmptyArrayAndDefaultMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode PUT: %v", err)
		}
		if got := strings.TrimSpace(string(raw["origins"])); got != "[]" {
			t.Errorf("origins JSON=%s, want []", got)
		}
		if got := strings.Trim(string(raw["preflight"]), `"`); got != string(BrowserPreflightPlatform) {
			t.Errorf("preflight=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"client","origins":[],"preflight":"platform","credentials":false}`))
	}))
	defer server.Close()

	ctx := &AppCtx{platform: newHTTPPlatformClient(server.URL, "app-token")}
	if _, err := ctx.ReplaceBrowserOriginPolicy("client", BrowserOriginPolicy{}); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceBrowserOriginPolicyRejectsInvalidMode(t *testing.T) {
	client := newHTTPPlatformClient("http://127.0.0.1:1", "app-token").(BrowserOriginPolicyClient)
	if _, err := client.ReplaceBrowserOriginPolicy("client", BrowserOriginPolicy{Preflight: "sidecar"}); err == nil || !strings.Contains(err.Error(), "invalid browser preflight mode") {
		t.Fatalf("invalid mode error=%v", err)
	}
}

func TestReplaceBrowserOriginPolicyDetectsOldServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Pre-policy servers ignored unknown request fields and returned only the
		// origin set. The SDK must not treat that as delegated-preflight success.
		_, _ = w.Write([]byte(`{"key":"api-1","origins":["https://api.example"]}`))
	}))
	defer server.Close()

	ctx := &AppCtx{platform: newHTTPPlatformClient(server.URL, "app-token")}
	_, err := ctx.ReplaceBrowserOriginPolicy("api-1", BrowserOriginPolicy{
		Origins:     []string{"https://api.example"},
		Preflight:   BrowserPreflightApp,
		Credentials: false,
	})
	if err == nil || !strings.Contains(err.Error(), "upgrade the Apteva server") {
		t.Fatalf("old server error=%v", err)
	}
}

func TestReplaceBrowserOriginsUsesExplicitEmptyArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("decode PUT: %v", err)
		}
		if got := strings.TrimSpace(string(raw["origins"])); got != "[]" {
			t.Errorf("origins JSON=%s, want []", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"client","origins":[]}`))
	}))
	defer server.Close()

	ctx := &AppCtx{platform: newHTTPPlatformClient(server.URL, "app-token")}
	if _, err := ctx.ReplaceBrowserOrigins("client", nil); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserOriginsOptionalCapabilityDoesNotExpandPlatformClient(t *testing.T) {
	ctx := (&AppCtx{platform: &stubProjectPlatformClient{}}).WithProject("project-a")
	if ctx.BrowserOriginsAPI() != nil {
		t.Fatal("plain PlatformClient unexpectedly implements BrowserOriginClient")
	}
	if ctx.BrowserOriginPolicyAPI() != nil {
		t.Fatal("plain PlatformClient unexpectedly implements BrowserOriginPolicyClient")
	}
	if _, err := ctx.ReplaceBrowserOrigins("client", []string{"https://app.example"}); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("unsupported ReplaceBrowserOrigins error=%v", err)
	}
	if err := ctx.DeleteBrowserOrigins("client"); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("unsupported DeleteBrowserOrigins error=%v", err)
	}
	if _, err := ctx.ListBrowserOriginRegistrations(); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("unsupported ListBrowserOriginRegistrations error=%v", err)
	}
	if _, err := ctx.ReplaceBrowserOriginPolicy("client", BrowserOriginPolicy{}); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("unsupported ReplaceBrowserOriginPolicy error=%v", err)
	}
}

func TestBrowserOriginRegistrationKeyValidation(t *testing.T) {
	httpClient := newHTTPPlatformClient("http://127.0.0.1:1", "app-token")
	client := httpClient.(BrowserOriginClient)
	policyClient := httpClient.(BrowserOriginPolicyClient)
	for _, key := range []string{"", "   ", "client/child", `client\child`} {
		if _, err := client.ReplaceBrowserOrigins(key, nil); err == nil {
			t.Fatalf("key %q accepted", key)
		}
		if err := client.DeleteBrowserOrigins(key); err == nil {
			t.Fatalf("delete key %q accepted", key)
		}
		if _, err := policyClient.ReplaceBrowserOriginPolicy(key, BrowserOriginPolicy{}); err == nil {
			t.Fatalf("policy key %q accepted", key)
		}
	}
}
