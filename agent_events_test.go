package sdk

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendTrackedAgentEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/apps/callback/agents/17/event" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["source_event_id"] != "tasks:occurrence:123" || body["track_lifecycle"] != true || body["thread_id"] != "main" {
			t.Fatalf("body = %#v", body)
		}
		writeJSONForAgentEventTest(w, map[string]any{
			"source_event_id": "tasks:occurrence:123",
			"execution_id":    "exe_123",
			"accepted":        true,
			"duplicate":       false,
			"thread_id":       "main",
		})
	}))
	defer server.Close()

	client := newHTTPPlatformClient(server.URL, "token").(AgentEventClient)
	receipt, err := client.SendTrackedAgentEvent(AgentEventRequest{
		AgentID: 17, ThreadID: "main", SourceEventID: " tasks:occurrence:123 ", Message: "work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ExecutionID != "exe_123" || !receipt.Accepted || receipt.Duplicate {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestSendTrackedAgentEventValidation(t *testing.T) {
	client := newHTTPPlatformClient("http://127.0.0.1:1", "token").(AgentEventClient)
	for _, request := range []AgentEventRequest{
		{},
		{AgentID: 1, SourceEventID: "event", Message: nil},
		{AgentID: 1, SourceEventID: "event", ThreadID: "bad/thread", Message: "work"},
		{AgentID: 1, SourceEventID: strings.Repeat("x", 1025), Message: "work"},
	} {
		if _, err := client.SendTrackedAgentEvent(request); err == nil {
			t.Fatalf("request unexpectedly valid: %#v", request)
		}
	}
}

func TestDecodeAgentEventLifecycle(t *testing.T) {
	wantTime := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	lifecycle, err := DecodeAgentEventLifecycle(Event{
		DeliveryID: "exe_123:3", Event: AgentEventLifecycleEvent,
		Data: map[string]any{
			"type": "event.settled", "source_event_id": "tasks:occurrence:123",
			"execution_id": "exe_123", "thread_id": "main",
			"timestamp": wantTime.Format(time.RFC3339), "sequence": float64(3),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.ID != "exe_123:3" || lifecycle.Type != "event.settled" || !lifecycle.Timestamp.Equal(wantTime) || lifecycle.Sequence != 3 {
		t.Fatalf("lifecycle = %#v", lifecycle)
	}
}

type eventDeliveryTestApp struct {
	handlers []EventHandler
}

func (a eventDeliveryTestApp) Manifest() Manifest {
	return Manifest{Name: "event-test", Version: "0.0.0"}
}
func (a eventDeliveryTestApp) OnMount(*AppCtx) error         { return nil }
func (a eventDeliveryTestApp) OnUnmount(*AppCtx) error       { return nil }
func (a eventDeliveryTestApp) HTTPRoutes() []Route           { return nil }
func (a eventDeliveryTestApp) MCPTools() []Tool              { return nil }
func (a eventDeliveryTestApp) Channels() []ChannelFactory    { return nil }
func (a eventDeliveryTestApp) Workers() []Worker             { return nil }
func (a eventDeliveryTestApp) EventHandlers() []EventHandler { return a.handlers }

func TestEventEndpointPropagatesHandlerFailure(t *testing.T) {
	mux := http.NewServeMux()
	mountFrameworkRoutes(mux, eventDeliveryTestApp{handlers: []EventHandler{{
		Event:   AgentEventLifecycleEvent,
		Handler: func(*AppCtx, Event) error { return errors.New("database unavailable") },
	}}}, &AppCtx{logger: silentLogger{}})
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(`{"delivery_id":"exe_1:1","event":"agent.event.lifecycle"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEventEndpointRequiresLifecycleHandler(t *testing.T) {
	mux := http.NewServeMux()
	mountFrameworkRoutes(mux, eventDeliveryTestApp{}, &AppCtx{logger: silentLogger{}})
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(`{"delivery_id":"exe_1:1","event":"agent.event.lifecycle"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func writeJSONForAgentEventTest(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
