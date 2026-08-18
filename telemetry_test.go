package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func telemetryTestClient(baseURL string) *httpPlatformClient {
	return &httpPlatformClient{
		baseURL:      baseURL,
		token:        "install-token",
		client:       &http.Client{Timeout: 5 * time.Second},
		slowClient:   &http.Client{Timeout: 5 * time.Second},
		streamClient: &http.Client{},
	}
}

func TestSubscribeTelemetryReceivesFilteredEvents(t *testing.T) {
	var gotQuery atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/telemetry" {
			http.NotFound(w, r)
			return
		}
		gotQuery.Store(r.URL.RawQuery)
		if r.Header.Get("Authorization") != "Bearer install-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		// Heartbeat comment first — clients must skip non-data lines.
		fmt.Fprint(w, ": keepalive\n\n")
		for i := 1; i <= 3; i++ {
			payload, _ := json.Marshal(TelemetryStreamEvent{
				ID: fmt.Sprint(i), AgentID: 41, ThreadID: "chat-conv-1",
				Type: "llm.tool_chunk", Data: json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)),
			})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
		// Keep the stream open until the client goes away, so this
		// test exercises a live stream rather than instant EOF.
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := telemetryTestClient(server.URL)
	ch, err := client.SubscribeTelemetry(ctx, TelemetrySubscription{
		Events: []string{"llm.tool_chunk"}, ThreadPrefix: "chat-", AgentID: 41,
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var received []TelemetryStreamEvent
	for len(received) < 3 {
		select {
		case ev := <-ch:
			received = append(received, ev)
		case <-ctx.Done():
			t.Fatalf("timed out with %d events", len(received))
		}
	}
	if received[0].AgentID != 41 || received[0].Type != "llm.tool_chunk" {
		t.Fatalf("unexpected event: %+v", received[0])
	}
	// The filters must ride the query string — they are enforced
	// server-side, so omitting them would subscribe to the firehose.
	query, _ := gotQuery.Load().(string)
	for _, want := range []string{"events=llm.tool_chunk", "thread_prefix=chat-", "agent_id=41"} {
		if !containsQueryParam(query, want) {
			t.Fatalf("query %q missing %q", query, want)
		}
	}
}

func containsQueryParam(query, kv string) bool {
	for _, part := range splitQuery(query) {
		if part == kv {
			return true
		}
	}
	return false
}

func splitQuery(query string) []string {
	var parts []string
	current := ""
	for _, r := range query {
		if r == '&' {
			parts = append(parts, current)
			current = ""
			continue
		}
		current += string(r)
	}
	return append(parts, current)
}

// A permanent refusal must surface at subscribe time — the consumer
// degrades immediately instead of ranging over a silently dead channel.
func TestSubscribeTelemetryFailsFastOnPermanentRefusal(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no", status)
		}))
		client := telemetryTestClient(server.URL)
		_, err := client.SubscribeTelemetry(context.Background(), TelemetrySubscription{
			Events: []string{"llm.tool_chunk"},
		})
		server.Close()
		if err == nil {
			t.Fatalf("status %d: expected a subscribe-time error", status)
		}
	}
}

// Transient drops reconnect with a fresh stream — no cursor, by design.
func TestSubscribeTelemetryReconnectsAfterDrop(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := connections.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		payload, _ := json.Marshal(TelemetryStreamEvent{
			ID: fmt.Sprint(n), Type: "llm.tool_chunk", AgentID: 41,
		})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		// Close immediately: every event arrives on its own connection,
		// so receiving two proves a reconnect happened.
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := telemetryTestClient(server.URL)
	ch, err := client.SubscribeTelemetry(ctx, TelemetrySubscription{Events: []string{"llm.tool_chunk"}})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	seen := 0
	for seen < 2 {
		select {
		case _, open := <-ch:
			if !open {
				t.Fatalf("channel closed after %d events", seen)
			}
			seen++
		case <-ctx.Done():
			t.Fatalf("timed out after %d events (connections=%d)", seen, connections.Load())
		}
	}
	if connections.Load() < 2 {
		t.Fatalf("connections = %d, want a reconnect", connections.Load())
	}
}

func TestSubscribeTelemetryClosesOnContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := telemetryTestClient(server.URL)
	ch, err := client.SubscribeTelemetry(ctx, TelemetrySubscription{Events: []string{"llm.tool_chunk"}})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	cancel()
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("expected channel close, got an event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel did not close after cancel")
	}
}

// The scoped wrapper forwards the optional surface — a project-scoped
// app keeps telemetry when the inner client supports it.
func TestProjectScopedClientForwardsTelemetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scoped := &projectScopedClient{inner: telemetryTestClient(server.URL), projectID: "proj-1"}
	var asInterface PlatformClient = scoped
	tc, ok := asInterface.(TelemetryClient)
	if !ok {
		t.Fatal("projectScopedClient must satisfy TelemetryClient")
	}
	if _, err := tc.SubscribeTelemetry(ctx, TelemetrySubscription{Events: []string{"llm.tool_chunk"}}); err != nil {
		t.Fatalf("scoped subscribe: %v", err)
	}
}
