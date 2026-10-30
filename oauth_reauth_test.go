package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestHTTPPlatformClientReauthOAuthContract(t *testing.T) {
	t.Setenv("APTEVA_INSTALL_ID", "42")
	var gotPath, gotMethod string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"connection_id":7,"authorize_url":"https://accounts.example/auth?scope=a+b","expires_at":"2026-10-30T10:00:00Z"}`))
	}))
	defer server.Close()

	platform := newHTTPPlatformClient(server.URL, "app-token")
	client, ok := platform.(OAuthReauthClient)
	if !ok {
		t.Fatal("http platform client does not implement OAuthReauthClient")
	}
	res, err := client.ReauthOAuth(OAuthReauthRequest{
		ConnectionID:   7,
		OptionalScopes: []string{"https://www.googleapis.com/auth/youtube.channel-memberships.creator"},
		ReturnURL:      "/api/apps/social/accounts/oauth_done",
	})
	if err != nil {
		t.Fatalf("ReauthOAuth: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/apps/callback/oauth/reauth" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	want := map[string]any{
		"connection_id":   float64(7),
		"optional_scopes": []any{"https://www.googleapis.com/auth/youtube.channel-memberships.creator"},
		"return_url":      "/api/apps/social/accounts/oauth_done",
	}
	if !reflect.DeepEqual(gotBody, want) {
		t.Fatalf("body = %#v, want %#v", gotBody, want)
	}
	if res.ConnectionID != 7 || res.AuthorizeURL != "https://accounts.example/auth?scope=a+b" || res.ExpiresAt != "2026-10-30T10:00:00Z" {
		t.Fatalf("result = %#v", res)
	}
}

func TestOAuthReauthRequestOmitsEmptyOptionalScopes(t *testing.T) {
	raw, err := json.Marshal(OAuthReauthRequest{ConnectionID: 3, ReturnURL: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"connection_id":3,"return_url":"/r"}` {
		t.Fatalf("json = %s", raw)
	}
}

type stubReauthPlatformClient struct {
	*stubProjectPlatformClient
	got OAuthReauthRequest
}

func (s *stubReauthPlatformClient) ReauthOAuth(req OAuthReauthRequest) (*OAuthStartResult, error) {
	s.got = req
	return &OAuthStartResult{ConnectionID: req.ConnectionID, AuthorizeURL: "https://auth"}, nil
}

func TestProjectScopedClientForwardsReauthOAuth(t *testing.T) {
	inner := &stubReauthPlatformClient{stubProjectPlatformClient: &stubProjectPlatformClient{}}
	scoped := &projectScopedClient{inner: inner, projectID: "project-a"}
	var platform PlatformClient = scoped
	client, ok := platform.(OAuthReauthClient)
	if !ok {
		t.Fatal("projectScopedClient does not implement OAuthReauthClient")
	}
	res, err := client.ReauthOAuth(OAuthReauthRequest{ConnectionID: 9, OptionalScopes: []string{"x"}, ReturnURL: "/done"})
	if err != nil {
		t.Fatalf("ReauthOAuth: %v", err)
	}
	if res.ConnectionID != 9 {
		t.Fatalf("result = %#v", res)
	}
	want := OAuthReauthRequest{ConnectionID: 9, OptionalScopes: []string{"x"}, ReturnURL: "/done", ProjectID: "project-a"}
	if !reflect.DeepEqual(inner.got, want) {
		t.Fatalf("forwarded = %#v, want %#v", inner.got, want)
	}

	// An explicit project is preserved.
	if _, err := client.ReauthOAuth(OAuthReauthRequest{ConnectionID: 9, ProjectID: "project-b"}); err != nil {
		t.Fatal(err)
	}
	if inner.got.ProjectID != "project-b" {
		t.Fatalf("project = %q", inner.got.ProjectID)
	}
}

func TestProjectScopedClientReauthOAuthUnsupportedInner(t *testing.T) {
	scoped := &projectScopedClient{inner: &stubProjectPlatformClient{}, projectID: "p"}
	if _, err := scoped.ReauthOAuth(OAuthReauthRequest{ConnectionID: 1}); err == nil {
		t.Fatal("expected unsupported error when inner client lacks ReauthOAuth")
	}
}
