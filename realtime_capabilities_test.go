package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func verifiedCapabilityFixture() *RealtimeCapabilities {
	return &RealtimeCapabilities{Version: 1, ThreadID: "voice", Status: "ready", GrantsVerified: true,
		GrantedTools: []string{"check_availability", "create_booking"}, GrantedMCP: []string{"bookings"},
		ConnectedMCP: []string{"bookings"}, RegisteredTools: []string{"check_availability", "create_booking"},
		PresentedTools: []string{"check_availability"}, PresentationVerified: true, SessionGeneration: 2, AppliedSessionGeneration: 2,
		ConfigurationRevision: 4, AppliedRevision: 4}
}

func TestRealtimeCapabilitiesCannotDeclareMissingBookingToolsHealthy(t *testing.T) {
	cap := verifiedCapabilityFixture()
	missing, err := cap.MissingPresentedTools("check_availability", "create_booking", "create_booking")
	if err != nil || strings.Join(missing, ",") != "create_booking" {
		t.Fatal(missing, err)
	}
	cap.PresentedTools = append(cap.PresentedTools, "create_booking")
	missing, err = cap.MissingPresentedTools("check_availability", "create_booking")
	if err != nil || missing == nil || len(missing) != 0 {
		t.Fatal(missing, err)
	}
	for _, mutate := range []func(*RealtimeCapabilities){
		func(c *RealtimeCapabilities) { c.Version = 2 }, func(c *RealtimeCapabilities) { c.Status = "pending" },
		func(c *RealtimeCapabilities) { c.PresentationVerified = false }, func(c *RealtimeCapabilities) { c.AppliedRevision-- },
		func(c *RealtimeCapabilities) { c.SessionGeneration++ }, func(c *RealtimeCapabilities) { c.PresentedTools = nil },
		func(c *RealtimeCapabilities) { c.RegisteredTools = nil }, func(c *RealtimeCapabilities) { c.ConnectedMCP = nil },
	} {
		cap := verifiedCapabilityFixture()
		mutate(cap)
		if missing, err := cap.MissingPresentedTools("check_availability"); err == nil || missing != nil {
			t.Fatal("unverified evidence reported healthy", cap, missing)
		}
	}
	var absent *RealtimeCapabilities
	if absent.Verified() {
		t.Fatal("nil snapshot verified")
	}
	if _, err := absent.MissingPresentedTools("check_availability"); err == nil {
		t.Fatal("legacy server treated as verified")
	}
}

func TestRealtimeCapabilitiesJSONPreservesUnknownAndVerifiedEmpty(t *testing.T) {
	cap := verifiedCapabilityFixture()
	cap.GrantedTools = []string{}
	cap.GrantedMCP = []string{}
	cap.ConnectedMCP = []string{}
	cap.RegisteredTools = []string{}
	cap.PresentedTools = []string{}
	raw, err := json.Marshal(RealtimeSpawnResult{Status: "created", AudioToken: "keep", Capabilities: cap})
	if err != nil {
		t.Fatal(err)
	}
	var result RealtimeSpawnResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Capabilities.Verified() || result.Capabilities.PresentedTools == nil || result.AudioToken != "keep" {
		t.Fatal(string(raw))
	}
	unknown := &RealtimeCapabilities{ThreadID: "voice", Status: "unknown", GrantsVerified: true, GrantedTools: []string{}, GrantedMCP: []string{"bookings"}}
	raw, _ = json.Marshal(unknown)
	if !strings.Contains(string(raw), `"presented_tools":null`) || !strings.Contains(string(raw), `"granted_tools":[]`) {
		t.Fatal(string(raw))
	}
}

func TestRealtimeCapabilitiesClientUsesReadOnlyScopedRoutes(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer app-token" {
			t.Error("not authenticated read-only", r.Method)
		}
		switch calls {
		case 1:
			if r.URL.Path != "/api/apps/callback/threads/voice/capabilities" || r.URL.Query().Get("agent_id") != "7" {
				t.Error(r.URL)
			}
		case 2:
			if r.URL.Path != "/api/apps/callback/runtimes/rt-1/agents/main/realtime/voice/capabilities" {
				t.Error(r.URL)
			}
		default:
			t.Error("unexpected retry")
		}
		_ = json.NewEncoder(w).Encode(verifiedCapabilityFixture())
	}))
	defer ts.Close()
	ctx := (&AppCtx{platform: newHTTPPlatformClient(ts.URL, "app-token")}).WithProject("proj-1")
	cap, err := GetRealtimeThreadCapabilities(ctx.PlatformAPI(), 7, "voice")
	if err != nil || !cap.Verified() {
		t.Fatal(cap, err)
	}
	runtimeReader, ok := ctx.RuntimeAPI().(RuntimeRealtimeCapabilitiesClient)
	if !ok {
		t.Fatal("project runtime wrapper lost optional reader")
	}
	cap, err = runtimeReader.GetRuntimeRealtimeThreadCapabilities("rt-1", "main", "voice")
	if err != nil || !cap.Verified() || calls != 2 {
		t.Fatal(cap, err, calls)
	}
	if _, err := GetRealtimeThreadCapabilities(ctx.PlatformAPI(), 7, "bad/thread"); err == nil || calls != 2 {
		t.Fatal("invalid id sent to server")
	}
}
