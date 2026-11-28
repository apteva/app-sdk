package sdk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A principal named by another app says so, and a platform-verified one
// does not.
func TestAssertedPrincipalCarriesTheAssertingApp(t *testing.T) {
	const token = "target-token"
	t.Setenv("APTEVA_APP_TOKEN", token)
	asserted := TrustedPrincipal{
		Version: TrustedPrincipalVersion, SubjectType: "api_customer", SubjectID: "cus_42", ProjectID: "project-a",
		AssertedByApp: "api", AssertedByInstallID: 559, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}
	got, err := PrincipalFromRequest(attachTestPrincipal(t, token, asserted))
	if err != nil || got == nil {
		t.Fatalf("principal=%+v err=%v", got, err)
	}
	if !got.Asserted() || got.AssertedByApp != "api" || got.AssertedByInstallID != 559 || got.SubjectID != "cus_42" {
		t.Fatalf("asserted principal = %+v", got)
	}
	verified := TrustedPrincipal{Version: TrustedPrincipalVersion, UserID: 7, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	got, err = PrincipalFromRequest(attachTestPrincipal(t, token, verified))
	if err != nil || got.Asserted() {
		t.Fatalf("platform-verified principal reported as asserted: %+v err=%v", got, err)
	}
	if (*TrustedPrincipal)(nil).Asserted() {
		t.Fatal("nil principal reported as asserted")
	}
}

// CallApp sends the asserted subject only when the context carries one.
func TestCallAppSendsAssertedSubjectFromContext(t *testing.T) {
	var seenType, seenID []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenType = append(seenType, r.Header.Get(HeaderAssertSubjectType))
		seenID = append(seenID, r.Header.Get(HeaderAssertSubjectID))
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}]}}`))
	}))
	defer server.Close()
	client := &httpPlatformClient{baseURL: server.URL, token: "app-token", client: server.Client(), slowClient: server.Client()}

	if _, err := client.CallAppContext(WithAssertedSubject(context.Background(), " api_customer ", "cus_42"), "mobile", "streams_list", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallAppContext(context.Background(), "mobile", "streams_list", nil); err != nil {
		t.Fatal(err)
	}
	// An incomplete subject is not sent at all.
	if _, err := client.CallAppContext(WithAssertedSubject(context.Background(), "api_customer", ""), "mobile", "streams_list", nil); err != nil {
		t.Fatal(err)
	}
	if len(seenType) != 3 || seenType[0] != "api_customer" || seenID[0] != "cus_42" || seenType[1] != "" || seenID[1] != "" || seenType[2] != "" || seenID[2] != "" {
		t.Fatalf("subject headers seen: types=%q ids=%q", seenType, seenID)
	}
}

func TestPrincipalsAssertPermissionIsListedAndDescribed(t *testing.T) {
	found := false
	for _, permission := range AllPermissions() {
		if permission == PermPrincipalsAssert {
			found = true
		}
	}
	if !found || PermissionDescription(PermPrincipalsAssert) == "" {
		t.Fatalf("listed=%v description=%q", found, PermissionDescription(PermPrincipalsAssert))
	}
}
