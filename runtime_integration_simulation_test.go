package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeIntegrationBindingSimulateFields(t *testing.T) {
	raw, err := json.Marshal(RuntimeIntegrationBinding{
		App: "telephony", Role: "carrier", Slug: "telnyx", Simulate: true,
		Seed: map[string][]map[string]any{"phone_numbers": {{"phone_number": "+15550100001"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"simulate":true`) || !strings.Contains(string(raw), `"seed":{"phone_numbers":[{"phone_number":"+15550100001"}]}`) {
		t.Fatalf("binding JSON = %s", raw)
	}
	plain, _ := json.Marshal(RuntimeIntegrationBinding{App: "telephony", Role: "carrier", Slug: "telnyx"})
	if strings.Contains(string(plain), "simulate") || strings.Contains(string(plain), "seed") {
		t.Fatalf("unsimulated binding JSON changed: %s", plain)
	}
}

func TestRuntimeIntegrationTriggerAndStateContracts(t *testing.T) {
	var trigger RuntimeIntegrationTriggerRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/callback/runtimes/rt-1/integrations/telnyx/triggers/incoming_call":
			if err := json.NewDecoder(r.Body).Decode(&trigger); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"result":{"call_control_id":"v3:mock-1"},"deliveries":[{"id":"d1","event":"call.initiated","event_id":"e1","state":"delivered","attempts":1,"status":204,"occurred_at":"2026-01-01T00:00:00Z"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/callback/runtimes/rt-1/integrations/telnyx/state":
			if r.URL.Query().Get("app") != "telephony" || r.URL.Query().Get("role") != "carrier" {
				t.Fatalf("state query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"slug":"telnyx","app":"telephony","role":"carrier","connection_id":9,"collections":{"calls":[{"call_control_id":"v3:mock-1","state":"answered"}]},"deliveries":[]}`))
		case r.URL.Path == "/api/apps/callback/runtimes/rt-1/integrations/telnyx/triggers/missing":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"trigger_failed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := (&AppCtx{platform: newHTTPPlatformClient(server.URL, "app-token")}).WithProject("proj-1")
	sim, ok := ctx.RuntimeAPI().(RuntimeIntegrationSimulationClient)
	if !ok {
		t.Fatal("project-scoped runtime client does not expose the simulation extension")
	}
	sel := RuntimeIntegrationSelector{App: "telephony", Role: "carrier"}
	result, err := sim.TriggerRuntimeIntegration("rt-1", "telnyx", "incoming_call", RuntimeIntegrationTriggerRequest{
		RuntimeIntegrationSelector: sel,
		Input:                      map[string]any{"to": "+15550100001", "from": "+15550100999"},
		Wait:                       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if trigger.App != "telephony" || trigger.Role != "carrier" || !trigger.Wait || trigger.Input["to"] != "+15550100001" {
		t.Fatalf("trigger request = %#v", trigger)
	}
	var call struct {
		ID string `json:"call_control_id"`
	}
	if err := json.Unmarshal(result.Result, &call); err != nil || call.ID != "v3:mock-1" {
		t.Fatalf("result = %s err=%v", result.Result, err)
	}
	if len(result.Deliveries) != 1 || result.Deliveries[0].Event != "call.initiated" || result.Deliveries[0].Status != 204 {
		t.Fatalf("deliveries = %#v", result.Deliveries)
	}

	state, err := sim.GetRuntimeIntegrationState("rt-1", "telnyx", sel)
	if err != nil {
		t.Fatal(err)
	}
	if state.ConnectionID != 9 || state.Collections["calls"][0]["state"] != "answered" {
		t.Fatalf("state = %#v", state)
	}

	if _, err := sim.TriggerRuntimeIntegration("rt-1", "telnyx", "missing", RuntimeIntegrationTriggerRequest{}); err == nil || !strings.Contains(err.Error(), "trigger_failed") {
		t.Fatalf("rejected trigger err = %v", err)
	}
}

func TestRuntimeIntegrationSimulationUnavailableWithoutExtension(t *testing.T) {
	scoped := &projectScopedClient{inner: &stubProjectPlatformClient{}, projectID: "proj-1"}
	if _, err := scoped.TriggerRuntimeIntegration("rt-1", "telnyx", "incoming_call", RuntimeIntegrationTriggerRequest{}); err == nil {
		t.Fatal("trigger without a simulation client succeeded")
	}
	if _, err := scoped.GetRuntimeIntegrationState("rt-1", "telnyx", RuntimeIntegrationSelector{}); err == nil {
		t.Fatal("state without a simulation client succeeded")
	}
}
