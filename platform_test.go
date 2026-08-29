// Tests for decodeMCPEnvelope. Targets the exact wire shapes
// CallAppResult is meant to handle:
//
//   1. Full JSON-RPC + MCP content envelope — what apteva-server's
//      callback proxy returns today
//   2. Pre-unwrapped inner JSON — what testkit fakes pass back
//   3. error envelope — RPC-level errors must surface, never out
//
// Bug here = silent zero values for every cross-app call.

package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetIntegrationURLProperty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/apps/callback/integrations/42/url-properties/content_delivery" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"integration":"tiktok-api","property":"content_delivery","ready":true,"configured_prefix":"https://agents.example/api/relay/","state":{"hosting_status":"ready","relay_status":"ready"}}`))
	}))
	defer ts.Close()
	status, err := GetIntegrationURLProperty(newHTTPPlatformClient(ts.URL, "test-token"), 42, "content_delivery")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.Integration != "tiktok-api" || status.State.HostingStatus != "ready" {
		t.Fatalf("status=%+v", status)
	}
}

func TestDecodeMCPEnvelope_FullEnvelope(t *testing.T) {
	raw := json.RawMessage(`{
		"jsonrpc":"2.0","id":1,
		"result":{"content":[{"type":"text","text":"{\"files\":[{\"id\":42,\"name\":\"x.mkv\"}]}"}]}
	}`)
	var out struct {
		Files []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := decodeMCPEnvelope(raw, "storage", "files_list", &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Files) != 1 || out.Files[0].ID != 42 || out.Files[0].Name != "x.mkv" {
		t.Errorf("decoded shape = %+v", out)
	}
}

func TestDecodeMCPEnvelope_AlreadyUnwrapped(t *testing.T) {
	// Test fakes / future platform versions might hand callers the
	// inner JSON directly. CallAppResult must still work.
	raw := json.RawMessage(`{"folders":["a","b","c"]}`)
	var out struct {
		Folders []string `json:"folders"`
	}
	if err := decodeMCPEnvelope(raw, "storage", "files_list_folders", &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Folders) != 3 || out.Folders[0] != "a" {
		t.Errorf("decoded shape = %+v", out)
	}
}

func TestDecodeMCPEnvelope_BareMCPResult(t *testing.T) {
	raw := json.RawMessage(`{"content":[{"type":"text","text":"{\"ok\":true}"}]}`)
	var out struct {
		OK bool `json:"ok"`
	}
	if err := decodeMCPEnvelope(raw, "trading", "portfolio_get", &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK {
		t.Fatalf("decoded OK=false")
	}
}

func TestDecodeMCPEnvelope_RPCError(t *testing.T) {
	raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`)
	var out map[string]any
	err := decodeMCPEnvelope(raw, "storage", "nope", &out)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Errorf("error didn't mention RPC message: %v", err)
	}
	if !strings.Contains(err.Error(), "storage.nope") {
		t.Errorf("error didn't mention app.tool: %v", err)
	}
}

func TestDecodeMCPEnvelope_EmptyText(t *testing.T) {
	raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":""}]}}`)
	var out map[string]any
	if err := decodeMCPEnvelope(raw, "x", "y", &out); err == nil {
		t.Errorf("expected error for empty content text")
	}
}

func TestDecodeMCPEnvelope_InvalidInnerJSON(t *testing.T) {
	raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"not json"}]}}`)
	var out map[string]any
	err := decodeMCPEnvelope(raw, "x", "y", &out)
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !strings.Contains(err.Error(), "decode inner JSON") {
		t.Errorf("error message format changed: %v", err)
	}
}

func TestDecodeMCPEnvelope_NilOut(t *testing.T) {
	raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}]}}`)
	if err := decodeMCPEnvelope(raw, "x", "y", nil); err == nil {
		t.Errorf("expected error for nil out")
	}
}

func TestEnvironmentClientCallAppResult(t *testing.T) {
	var sawAuth bool
	var sawPath bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/environments/env-1/seed" && r.Method == http.MethodPost {
			sawPath = true
			sawAuth = r.Header.Get("Authorization") == "Bearer test-token"
			var body struct {
				Calls []EnvironmentSeedCall `json:"calls"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if len(body.Calls) != 1 || body.Calls[0].App != "trading" || body.Calls[0].Tool != "portfolio_step" {
				t.Fatalf("unexpected seed body: %+v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []json.RawMessage{json.RawMessage(`{"content":[{"type":"text","text":"{\"status\":\"ok\"}"}]}`)},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	api := newHTTPPlatformClient(ts.URL, "test-token")
	var out struct {
		Status string `json:"status"`
	}
	if err := api.CallEnvironmentAppResult("env-1", "trading", "portfolio_step", map[string]any{"tick": 1}, &out); err != nil {
		t.Fatalf("CallEnvironmentAppResult: %v", err)
	}
	if !sawPath {
		t.Fatalf("environment seed endpoint was not called")
	}
	if !sawAuth {
		t.Fatalf("Authorization header not forwarded")
	}
	if out.Status != "ok" {
		t.Fatalf("status=%q, want ok", out.Status)
	}
}

func TestRealtimeLifecycleRequestsCarryAgentIdentity(t *testing.T) {
	var renewed, killed bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("agent_id") != "42" {
			t.Fatalf("agent_id=%q", r.URL.Query().Get("agent_id"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/callback/threads/voice-one/audio-token":
			renewed = true
			_ = json.NewEncoder(w).Encode(RealtimeSpawnResult{Status: "renewed", ThreadID: "voice-one", AudioBridgeURL: "wss://bridge.test"})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/apps/callback/threads/voice-one":
			killed = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	api := newHTTPPlatformClient(ts.URL, "test-token")
	if _, err := api.RenewRealtimeAudioBridge(42, "voice-one"); err != nil {
		t.Fatal(err)
	}
	if err := api.KillThread(42, "voice-one"); err != nil {
		t.Fatal(err)
	}
	if !renewed || !killed {
		t.Fatalf("renewed=%t killed=%t", renewed, killed)
	}
}

func TestOpaqueThreadRequestsPreserveTargetAndStructuredMessage(t *testing.T) {
	var sent, spawned bool
	eventID := "conversation:42:message:99:agent:7"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/callback/agents/42/event":
			var body struct {
				ThreadID string         `json:"thread_id"`
				Message  map[string]any `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.ThreadID != "opaque-a7" || body.Message["type"] != "work.ready" {
				t.Fatalf("event body=%+v", body)
			}
			sent = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/callback/threads/spawn":
			var body ThreadSpawnRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.ProjectID != "project-7" {
				t.Fatalf("spawn project_id=%q, want project-7", body.ProjectID)
			}
			if body.AgentID != 42 || body.ThreadID != "opaque-run-9" || body.DirectiveSuffix != "Do the app-owned work" {
				t.Fatalf("spawn body=%+v", body)
			}
			if len(body.Events) != 1 || body.Events[0].ID != eventID || body.Events[0].Message != "Hello" {
				t.Fatalf("spawn events=%+v", body.Events)
			}
			spawned = true
			_ = json.NewEncoder(w).Encode(ThreadSpawnResult{
				Status: "created", Thread: ThreadRef{AgentID: 42, ThreadID: body.ThreadID},
				Events: ThreadEventReceipt{Accepted: []string{eventID}, Duplicates: []string{"already-seen"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := newHTTPPlatformClient(ts.URL, "test-token").(ThreadClient)
	if err := client.SendThreadEvent(ThreadRef{AgentID: 42, ThreadID: "opaque-a7"}, map[string]any{"type": "work.ready"}); err != nil {
		t.Fatal(err)
	}
	result, err := client.SpawnThread(ThreadSpawnRequest{
		AgentID: 42, ThreadID: "opaque-run-9", ProjectID: "project-7", DirectiveSuffix: "Do the app-owned work",
		Events: []ThreadEvent{{ID: eventID, Message: "Hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Thread.ThreadID != "opaque-run-9" || len(result.Events.Accepted) != 1 ||
		result.Events.Accepted[0] != eventID || len(result.Events.Duplicates) != 1 || !sent || !spawned {
		t.Fatalf("result=%+v sent=%t spawned=%t", result, sent, spawned)
	}
}

func TestSpawnThreadEventlessRequestRemainsCompatible(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, exists := body["events"]; exists {
			t.Fatalf("eventless spawn serialized events: %s", body["events"])
		}
		_ = json.NewEncoder(w).Encode(ThreadSpawnResult{
			Status: "exists", Thread: ThreadRef{AgentID: 9, ThreadID: "legacy-thread"},
		})
	}))
	defer ts.Close()

	client := newHTTPPlatformClient(ts.URL, "test-token").(ThreadClient)
	result, err := client.SpawnThread(ThreadSpawnRequest{AgentID: 9, ThreadID: "legacy-thread"})
	if err != nil || result.Status != "exists" || len(result.Events.Accepted) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestEnsureThreadReconcilesProfileAndEvents(t *testing.T) {
	eventID := "conversation:1:message:2:agent:3"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/apps/callback/threads/ensure" {
			http.NotFound(w, r)
			return
		}
		var req ThreadEnsureRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.AgentID != 3 || req.ThreadID != "chat-1" || req.ProfileHash != "hash-1" || len(req.Events) != 1 {
			t.Fatalf("request=%+v", req)
		}
		_ = json.NewEncoder(w).Encode(ThreadEnsureResult{Status: "updated", Reconciled: true,
			Thread: ThreadRef{AgentID: 3, ThreadID: "chat-1"}, ProfileHash: "hash-1",
			Events: ThreadEventReceipt{Accepted: []string{eventID}}})
	}))
	defer ts.Close()
	client := newHTTPPlatformClient(ts.URL, "test-token").(ThreadProfileClient)
	result, err := client.EnsureThread(ThreadEnsureRequest{ThreadSpawnRequest: ThreadSpawnRequest{
		AgentID: 3, ThreadID: "chat-1", DirectiveSuffix: "reply here",
		Events: []ThreadEvent{{ID: eventID, Message: "hello"}},
	}, ProfileHash: "hash-1"})
	if err != nil || !result.Reconciled || result.ProfileHash != "hash-1" || len(result.Events.Accepted) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestListIngressRoutesDecodesNativeCertificateStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/apps/callback/ingress/routes" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"routes": []map[string]any{{
				"id":         7,
				"hostname":   "app.example.com",
				"tls_mode":   "auto",
				"status":     "active",
				"project_id": "proj-1",
				"certificate": map[string]any{
					"fqdn":      "app.example.com",
					"status":    "live",
					"not_after": "2027-01-01T00:00:00Z",
				},
			}},
		})
	}))
	defer ts.Close()

	routes, err := newHTTPPlatformClient(ts.URL, "test-token").ListIngressRoutes()
	if err != nil {
		t.Fatalf("ListIngressRoutes: %v", err)
	}
	if len(routes) != 1 || routes[0].Certificate == nil {
		t.Fatalf("routes = %+v", routes)
	}
	if routes[0].Certificate.Status != "live" || routes[0].Certificate.NotAfter != "2027-01-01T00:00:00Z" {
		t.Fatalf("certificate = %+v", routes[0].Certificate)
	}
}

func TestIntegrationWebhookLifecycleRequests(t *testing.T) {
	var ensured, verified bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization=%q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/apps/callback/integration-webhooks/ensure":
			var req IntegrationWebhookEnsureRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.ConnectionID != 57 || req.Role != "payment_processor" ||
				req.CallbackPath != "/webhooks/stripe" {
				t.Fatalf("ensure request=%#v", req)
			}
			ensured = true
			_ = json.NewEncoder(w).Encode(IntegrationWebhookStatus{
				ConnectionID: 57, Role: req.Role, Provider: "stripe", Status: "ready",
			})
		case "/api/apps/callback/integration-webhooks/verify":
			var req IntegrationWebhookVerifyRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.Payload != `{"id":"evt_1"}` || req.Signature != "stripe-signature" {
				t.Fatalf("verify request=%#v", req)
			}
			verified = true
			_ = json.NewEncoder(w).Encode(IntegrationWebhookVerifyResult{
				Provider: "stripe", Event: json.RawMessage(req.Payload),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	api := newHTTPPlatformClient(ts.URL, "test-token")
	status, err := api.EnsureIntegrationWebhook(IntegrationWebhookEnsureRequest{
		ConnectionID: 57,
		Role:         "payment_processor",
		CallbackPath: "/webhooks/stripe",
		Events:       []string{"checkout.session.completed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "ready" {
		t.Fatalf("ensure status=%#v", status)
	}
	result, err := api.VerifyIntegrationWebhook(IntegrationWebhookVerifyRequest{
		Role: "payment_processor", Payload: `{"id":"evt_1"}`, Signature: "stripe-signature",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "stripe" || string(result.Event) != `{"id":"evt_1"}` {
		t.Fatalf("verify result=%#v", result)
	}
	if !ensured || !verified {
		t.Fatalf("ensured=%t verified=%t", ensured, verified)
	}
}

func TestManagedConnectionLifecycleUsesOptionalCapability(t *testing.T) {
	var ensured, rotated, revoked bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization=%q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/callback/connections/managed/ensure":
			var req ManagedConnectionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.Key != "phone:customer-1" || req.ProjectID != "project-1" || req.Fields["auth_token"] != "secret" {
				t.Fatalf("ensure request=%#v", req)
			}
			ensured = true
			_ = json.NewEncoder(w).Encode(PlatformConnection{ID: 57, AppSlug: "twilio", CredentialManagement: "app", ExportPolicy: ExportNever})
		case r.Method == http.MethodPut && r.URL.Path == "/api/apps/callback/connections/57/managed/credentials":
			var req ManagedConnectionRotation
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.Fields["auth_token"] != "rotated" {
				t.Fatalf("rotation=%#v", req)
			}
			rotated = true
			_ = json.NewEncoder(w).Encode(PlatformConnection{ID: 57, AppSlug: "twilio", CredentialManagement: "app", ExportPolicy: ExportNever})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/apps/callback/connections/57/managed":
			revoked = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	base := newHTTPPlatformClient(ts.URL, "test-token")
	scoped := wrapPlatformWithProject(base, "project-1")
	conn, err := EnsureManagedConnection(scoped, ManagedConnectionRequest{
		Key: "phone:customer-1", AppSlug: "twilio", Name: "Managed Phone",
		Fields: map[string]string{"auth_token": "secret"},
	})
	if err != nil || conn.ID != 57 || conn.ExportPolicy != ExportNever {
		t.Fatalf("ensure conn=%#v err=%v", conn, err)
	}
	conn, err = RotateManagedConnection(scoped, 57, ManagedConnectionRotation{Fields: map[string]string{"auth_token": "rotated"}})
	if err != nil || conn.ID != 57 {
		t.Fatalf("rotate conn=%#v err=%v", conn, err)
	}
	if err := RevokeManagedConnection(scoped, 57); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !ensured || !rotated || !revoked {
		t.Fatalf("ensured=%t rotated=%t revoked=%t", ensured, rotated, revoked)
	}
}

func TestManagedTenantControlUsesOptionalCapability(t *testing.T) {
	seen := map[string]bool{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization=%q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/callback/managed-tenants/tenants":
			seen["tenant"] = true
			_ = json.NewEncoder(w).Encode(ManagedTenant{TenantID: "tenant-1", AccountID: "account-1", Status: "active"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/callback/managed-tenants/enrollments":
			seen["enrollment"] = true
			_ = json.NewEncoder(w).Encode(ManagedTenantEnrollment{TenantID: "tenant-1", Ticket: "secret-ticket"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/callback/managed-tenants/grants":
			seen["grant"] = true
			_ = json.NewEncoder(w).Encode(ManagedConnectionGrant{TenantID: "tenant-1", GrantID: "phone", Status: "active"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/callback/managed-tenants/grants/tenant-1/phone/delivery":
			seen["delivery"] = true
			_ = json.NewEncoder(w).Encode(ManagedConnectionGrantDelivery{TenantID: "tenant-1", GrantID: "phone", ControllerToken: "aptg_secret"})
		case r.Method == http.MethodDelete && r.URL.EscapedPath() == "/api/apps/callback/managed-tenants/grants/tenant-1/phone%2Fprimary":
			seen["revoke"] = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/callback/managed-tenants/bundles":
			seen["bundle"] = true
			_ = json.NewEncoder(w).Encode(ManagedTenantBundle{TenantID: "tenant-1", BundleID: "phone", Revision: 1})
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/callback/managed-tenants/bundles/tenant-1/phone":
			seen["get-bundle"] = true
			_ = json.NewEncoder(w).Encode(ManagedTenantBundle{TenantID: "tenant-1", BundleID: "phone", Revision: 1, Status: "applied"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client, err := ManagedTenants(wrapPlatformWithProject(newHTTPPlatformClient(ts.URL, "test-token"), "project-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.EnsureManagedTenant(ManagedTenantRequest{TenantID: "tenant-1", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateManagedTenantEnrollment(ManagedTenantEnrollmentRequest{TenantID: "tenant-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.EnsureManagedConnectionGrant(ManagedConnectionGrantRequest{TenantID: "tenant-1", GrantID: "phone", ConnectionID: 57, AppSlug: "twilio"}); err != nil {
		t.Fatal(err)
	}
	if delivery, err := client.GetManagedConnectionGrantDelivery("tenant-1", "phone"); err != nil || delivery.ControllerToken != "aptg_secret" {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
	if err := client.RevokeManagedConnectionGrant("tenant-1", "phone/primary"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.EnsureManagedTenantBundle(ManagedTenantBundleRequest{TenantID: "tenant-1", BundleID: "phone"}); err != nil {
		t.Fatal(err)
	}
	if bundle, err := client.GetManagedTenantBundle("tenant-1", "phone"); err != nil || bundle.Status != "applied" {
		t.Fatalf("get bundle=%+v err=%v", bundle, err)
	}
	for _, key := range []string{"tenant", "enrollment", "grant", "delivery", "revoke", "bundle", "get-bundle"} {
		if !seen[key] {
			t.Fatalf("%s endpoint was not called", key)
		}
	}
}

func TestGetConnectionPublicConfig(t *testing.T) {
	var called bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/apps/callback/connections/57/public-config" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization=%q", r.Header.Get("Authorization"))
		}
		called = true
		_ = json.NewEncoder(w).Encode(ConnectionPublicConfig{
			ConnectionID: 57,
			Slug:         "stripe",
			Fields:       map[string]string{"publishableKey": "pk_test_browser"},
		})
	}))
	defer ts.Close()

	config, err := GetConnectionPublicConfig(newHTTPPlatformClient(ts.URL, "test-token"), 57)
	if err != nil {
		t.Fatal(err)
	}
	if !called || config.Fields["publishableKey"] != "pk_test_browser" {
		t.Fatalf("unexpected public config: %#v", config)
	}
}
