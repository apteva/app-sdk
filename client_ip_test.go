package sdk

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const clientIPTestToken = "test-installation-token"

func clientIPTestRequest() *http.Request {
	return httptest.NewRequest("GET", "http://app/softphone/media/call/session?transport=websocket", nil)
}

func TestClientIPMetadataSignedAddresses(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", clientIPTestToken)
	for _, tc := range []struct{ ip, want string }{
		{"198.51.100.7", "198.51.100.7"},
		{"2001:db8::7", "2001:db8::7"},
		{"::ffff:198.51.100.7", "198.51.100.7"},
	} {
		t.Run(tc.ip, func(t *testing.T) {
			r := clientIPTestRequest()
			r.Header.Set("Authorization", "Bearer unchanged")
			r.Header.Set("Upgrade", "websocket")
			SetClientIPHeaders(r, tc.ip, clientIPTestToken)
			ip, err := ClientIPFromRequest(r)
			if err != nil || ip != tc.want {
				t.Fatalf("address %q, error %v", ip, err)
			}
			if r.Header.Get("Authorization") != "Bearer unchanged" || r.Header.Get("Upgrade") != "websocket" {
				t.Fatal("unrelated handshake headers changed")
			}
		})
	}
}

func TestClientIPMetadataMissingAndUnsignedNeverTrusted(t *testing.T) {
	r := clientIPTestRequest()
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	r.Header.Set("X-Apteva-Client-IP", "198.51.100.7")
	if ip, err := ClientIPFromRequest(r); err != nil || ip != "" {
		t.Fatalf("unsigned address trusted: %q %v", ip, err)
	}
	if ip, err := ClientIPFromRequest(nil); err != nil || ip != "" {
		t.Fatal(ip, err)
	}
}

func TestClientIPMetadataInvalidAssertions(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", clientIPTestToken)
	for _, tc := range []struct {
		name   string
		change func(*http.Request)
	}{
		{"forged_signature", func(r *http.Request) { r.Header.Set(HeaderClientIPSignature, strings.Repeat("0", 64)) }},
		{"wrong_installation", func(r *http.Request) { SetClientIPHeaders(r, "198.51.100.7", "another-token") }},
		{"missing_signature", func(r *http.Request) { r.Header.Del(HeaderClientIPSignature) }},
		{"missing_payload", func(r *http.Request) { r.Header.Del(HeaderClientIPMetadata) }},
		{"duplicate_payload", func(r *http.Request) { r.Header.Add(HeaderClientIPMetadata, r.Header.Get(HeaderClientIPMetadata)) }},
		{"duplicate_signature", func(r *http.Request) { r.Header.Add(HeaderClientIPSignature, r.Header.Get(HeaderClientIPSignature)) }},
		{"wrong_method", func(r *http.Request) { r.Method = "POST" }},
		{"rewritten_path", func(r *http.Request) { r.URL.Path = "/another/path" }},
		{"changed_query", func(r *http.Request) { r.URL.RawQuery = "transport=webrtc" }},
		{"oversized", func(r *http.Request) { r.Header.Set(HeaderClientIPMetadata, strings.Repeat("x", 16385)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := clientIPTestRequest()
			SetClientIPHeaders(r, "198.51.100.7", clientIPTestToken)
			tc.change(r)
			if ip, err := ClientIPFromRequest(r); err == nil || ip != "" {
				t.Fatalf("invalid assertion accepted: %q %v", ip, err)
			}
		})
	}
	t.Setenv("APTEVA_APP_TOKEN", "")
	r := clientIPTestRequest()
	SetClientIPHeaders(r, "198.51.100.7", clientIPTestToken)
	if _, err := ClientIPFromRequest(r); err == nil {
		t.Fatal("tokenless verifier accepted an assertion")
	}
}

func TestClientIPMetadataSignedMalformedPayloads(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", clientIPTestToken)
	for _, tc := range []struct {
		name   string
		change func(*clientIPMetadata)
	}{
		{"expired", func(m *clientIPMetadata) { m.ExpiresAt = time.Now().Add(-time.Second).Unix() }},
		{"unsupported_version", func(m *clientIPMetadata) { m.Version = 2 }},
		{"invalid_address", func(m *clientIPMetadata) { m.IP = "invalid" }},
		{"zone_address", func(m *clientIPMetadata) { m.IP = "fe80::7%en0" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := clientIPTestRequest()
			m := clientIPMetadata{Version: 1, IP: "198.51.100.7", Method: r.Method, Target: r.URL.RequestURI(), ExpiresAt: time.Now().Add(time.Minute).Unix()}
			tc.change(&m)
			raw, _ := json.Marshal(m)
			payload := base64.RawURLEncoding.EncodeToString(raw)
			r.Header.Set(HeaderClientIPMetadata, payload)
			r.Header.Set(HeaderClientIPSignature, hex.EncodeToString(clientIPMAC(clientIPTestToken, payload)))
			if ip, err := ClientIPFromRequest(r); err == nil || ip != "" {
				t.Fatalf("malformed assertion accepted: %q %v", ip, err)
			}
		})
	}
}

func TestSetClientIPHeadersScrubsCallerAssertions(t *testing.T) {
	for _, tc := range []struct{ ip, token string }{{"198.51.100.7", clientIPTestToken}, {"198.51.100.7", ""}, {"invalid", clientIPTestToken}, {"fe80::7%en0", clientIPTestToken}} {
		r := clientIPTestRequest()
		r.Header.Set(HeaderClientIPMetadata, "caller-payload")
		r.Header.Set(HeaderClientIPSignature, "caller-signature")
		r.Header.Set("X-Apteva-Client-IP", "caller-address")
		SetClientIPHeaders(r, tc.ip, tc.token)
		if r.Header.Get("X-Apteva-Client-IP") != "" || r.Header.Get(HeaderClientIPMetadata) == "caller-payload" || r.Header.Get(HeaderClientIPSignature) == "caller-signature" {
			t.Fatal("caller assertion retained")
		}
		if tc.token == "" || tc.ip != "198.51.100.7" {
			if r.Header.Get(HeaderClientIPMetadata) != "" || r.Header.Get(HeaderClientIPSignature) != "" {
				t.Fatal("invalid input left an assertion")
			}
		}
	}
	SetClientIPHeaders(nil, "198.51.100.7", clientIPTestToken)
}
