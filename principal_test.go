package sdk

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func attachTestPrincipal(t *testing.T, token string, principal TrustedPrincipal) *http.Request {
	t.Helper()
	raw, err := json.Marshal(principal)
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(payload))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(HeaderTrustedPrincipal, payload)
	req.Header.Set(HeaderTrustedPrincipalSignature, hex.EncodeToString(mac.Sum(nil)))
	return req
}

func TestPrincipalFromRequestVerifiesIdentityAndExpiry(t *testing.T) {
	const token = "target-token"
	t.Setenv("APTEVA_APP_TOKEN", token)
	want := TrustedPrincipal{Version: TrustedPrincipalVersion, UserID: 7, Email: "person@example.test", ProjectID: "project-a", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	req := attachTestPrincipal(t, token, want)
	got, err := PrincipalFromRequest(req)
	if err != nil || got == nil || got.UserID != want.UserID || got.Email != want.Email || got.ProjectID != want.ProjectID {
		t.Fatalf("principal=%+v err=%v", got, err)
	}
	req.Header.Set(HeaderTrustedPrincipalSignature, "00")
	if _, err := PrincipalFromRequest(req); err == nil {
		t.Fatal("tampered principal accepted")
	}
	expired := want
	expired.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	if _, err := PrincipalFromRequest(attachTestPrincipal(t, token, expired)); err == nil {
		t.Fatal("expired principal accepted")
	}
}

func TestSignedPrincipalIsAuthoritativeForCaller(t *testing.T) {
	const token = "target-token"
	t.Setenv("APTEVA_APP_TOKEN", token)
	req := attachTestPrincipal(t, token, TrustedPrincipal{Version: TrustedPrincipalVersion, UserID: 8, Email: "signed@example.test", ProjectID: "signed-project", ExpiresAt: time.Now().Add(time.Minute).Unix()})
	req.Header.Set("X-Apteva-Subject-Type", "user")
	req.Header.Set("X-Apteva-Subject-ID", "999")
	req.Header.Set("X-Apteva-Project-ID", "spoofed")
	h := &mcpHandler{ctx: &AppCtx{manifest: &Manifest{}}}
	caller := h.buildCaller(req)
	if caller == nil || caller.SubjectID != "8" || caller.SubjectEmail != "signed@example.test" || caller.ProjectID != "signed-project" {
		t.Fatalf("caller=%+v", caller)
	}
	// A bad signed representation never falls back to unsigned identity.
	req.Header.Set(HeaderTrustedPrincipalSignature, "00")
	caller = h.buildCaller(req)
	if caller != nil && caller.SubjectID != "" {
		t.Fatalf("unsigned fallback accepted: %+v", caller)
	}
}
