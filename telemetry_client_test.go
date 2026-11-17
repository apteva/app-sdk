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

func TestTelemetryRemoteClientAndOptionalLocalScope(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer private-key" {
			t.Error("missing credential")
		}
		if r.URL.Query().Get("project_id") != "p" {
			t.Error("project not forwarded")
		}
		if r.URL.Path != "/api/telemetry" && r.URL.Path != "/api/apps/callback/telemetry/history" {
			t.Error("wrong read route", r.URL.Path)
		}
		json.NewEncoder(w).Encode(TelemetryPage{ServerID: "s", Events: []TelemetryStreamEvent{{ID: "event"}}})
	}))
	defer server.Close()
	remote, e := NewTelemetryClient(server.URL, "private-key")
	if e != nil {
		t.Fatal(e)
	}
	page, e := remote.QueryTelemetry(context.Background(), TelemetryQuery{ProjectID: "p"})
	if e != nil || page.Events[0].ID != "event" {
		t.Fatal(page, e)
	}
	platform := newHTTPPlatformClient(server.URL, "private-key")
	scoped := &projectScopedClient{inner: platform, projectID: "p"}
	app := NewAppCtxForTest(nil, nil, nil, scoped, nil)
	if app.TelemetryAPI() == nil {
		t.Fatal("optional client unavailable")
	}
	if _, e = app.TelemetryAPI().QueryTelemetry(context.Background(), TelemetryQuery{}); e != nil {
		t.Fatal(e)
	}
	before := calls.Load()
	if _, e = app.TelemetryAPI().QueryTelemetry(context.Background(), TelemetryQuery{ProjectID: "other"}); e == nil || calls.Load() != before {
		t.Fatal("project override reached network")
	}
	for _, origin := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/?key=secret"} {
		if _, e = NewTelemetryClient(origin, "key"); e == nil {
			t.Fatal("unsafe origin accepted", origin)
		}
	}
}
func TestTelemetryWatchResumesDeduplicatesAndReportsGap(t *testing.T) {
	var requests atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/telemetry/stream" || r.URL.Query().Get("format") != "delivery" {
			t.Error("watch did not use the existing telemetry stream", r.URL.Path)
		}
		n := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v TelemetryDelivery) {
			raw, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", raw)
			w.(http.Flusher).Flush()
		}
		if n == 1 {
			if r.URL.Query().Get("backfill") != "true" {
				t.Error("missing backfill")
			}
			send(TelemetryDelivery{Kind: "ready", ServerID: "server", Cursor: "start"})
			send(TelemetryDelivery{Kind: "history", ServerID: "server", History: &TelemetryPage{Events: []TelemetryStreamEvent{{ServerID: "server", ID: "overlap"}}}})
			send(TelemetryDelivery{Kind: "event", ServerID: "server", Cursor: "one", Event: &TelemetryStreamEvent{ID: "overlap"}})
			send(TelemetryDelivery{Kind: "event", ServerID: "server", Cursor: "two", Event: &TelemetryStreamEvent{ID: "live"}})
			return
		}
		if r.URL.Query().Get("cursor") != "two" || r.URL.Query().Get("backfill") != "true" {
			t.Error("resume state", r.URL.RawQuery)
		}
		send(TelemetryDelivery{Kind: "gap", ServerID: "server", Cursor: "new-epoch", Reason: "server_restarted"})
		send(TelemetryDelivery{Kind: "event", ServerID: "server", Cursor: "three", Event: &TelemetryStreamEvent{ID: "after"}})
		<-r.Context().Done()
	}))
	defer server.Close()
	client, _ := NewTelemetryClient(server.URL, "key")
	stream, e := client.WatchTelemetry(ctx, TelemetryWatch{Query: TelemetryQuery{Events: []string{"llm.done"}}, Backfill: true})
	if e != nil {
		t.Fatal(e)
	}
	ids := []string{}
	gap := false
	for frame := range stream {
		if frame.Event != nil {
			ids = append(ids, frame.Event.ID)
			if frame.Event.ID == "after" {
				cancel()
				break
			}
		}
		if frame.Kind == "gap" {
			gap = frame.Reason == "server_restarted"
		}
	}
	if fmt.Sprint(ids) != "[live after]" || !gap || requests.Load() != 2 {
		t.Fatal("resumable stream", ids, gap, requests.Load())
	}
}
func TestTelemetryWatchDoesNotSilentlyDropSlowConsumerEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 180; i++ {
			raw, _ := json.Marshal(TelemetryDelivery{Kind: "event", ServerID: "s", Cursor: fmt.Sprint(i), Event: &TelemetryStreamEvent{ID: fmt.Sprint(i)}})
			fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client, _ := NewTelemetryClient(server.URL, "key")
	stream, e := client.WatchTelemetry(ctx, TelemetryWatch{})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 180; i++ {
		frame := <-stream
		if frame.Event == nil || frame.Event.ID != fmt.Sprint(i) {
			t.Fatalf("event %d dropped: %+v", i, frame)
		}
	}
	cancel()
}
func TestTelemetryRedirectDoesNotForwardCredentials(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer origin.Close()
	client, _ := NewTelemetryClient(origin.URL, "private")
	if _, e := client.ListObservedRuntimes(context.Background(), "p"); e == nil || reached.Load() {
		t.Fatal("credentialed redirect followed")
	}
}

func TestTelemetryRemoteLegacySubscriptionUsesExistingStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/telemetry/stream" || r.URL.Query().Get("all") != "1" || r.URL.Query().Get("format") != "" || r.URL.Query().Get("replay") != "false" {
			t.Error("legacy subscription route", r.URL.String())
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"one\",\"instance_id\":41,\"type\":\"llm.tool_chunk\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := NewTelemetryClient(server.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	events, err := client.SubscribeTelemetry(ctx, TelemetrySubscription{Events: []string{"llm.tool_chunk"}})
	if err != nil {
		t.Fatal(err)
	}
	event, ok := <-events
	if !ok || event.ID != "one" {
		t.Fatal("legacy subscription", event, ok)
	}
	cancel()
}
