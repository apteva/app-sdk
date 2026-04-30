package sdk

// httpEmitter behaviour:
//   - POSTs to <gateway>/api/app-events/internal/emit with the
//     correct Authorization header
//   - Body has {topic, data}
//   - No-op when gateway/token unconfigured (tests, manifests w/o
//     platform plumbing) — must NOT panic
//   - Empty topic is dropped silently
//   - Underlying request times out at 100ms, never blocks the
//     caller

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmit_PostsToCorrectURLWithToken(t *testing.T) {
	type seen struct {
		path  string
		auth  string
		ctype string
		body  map[string]any
	}
	var got atomic.Pointer[seen]
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		got.Store(&seen{
			path:  r.URL.Path,
			auth:  r.Header.Get("Authorization"),
			ctype: r.Header.Get("Content-Type"),
			body:  parsed,
		})
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	e := newHTTPEmitter(ts.URL, "tok-abc", silentLogger{})
	e.Emit("file.added", map[string]any{"id": 7, "name": "x.pdf"})

	// Emit fans the request off in a goroutine — wait briefly.
	if !waitFor(func() bool { return got.Load() != nil }, 500*time.Millisecond) {
		t.Fatal("server never received emit POST")
	}
	g := got.Load()
	if g.path != "/api/app-events/internal/emit" {
		t.Errorf("path = %q, want /api/app-events/internal/emit", g.path)
	}
	if g.auth != "Bearer tok-abc" {
		t.Errorf("auth = %q, want Bearer tok-abc", g.auth)
	}
	if g.ctype != "application/json" {
		t.Errorf("ctype = %q, want application/json", g.ctype)
	}
	if g.body["topic"] != "file.added" {
		t.Errorf("body.topic = %v", g.body["topic"])
	}
	data, ok := g.body["data"].(map[string]any)
	if !ok {
		t.Fatalf("body.data not an object: %v", g.body["data"])
	}
	if data["name"] != "x.pdf" {
		t.Errorf("body.data.name = %v", data["name"])
	}
}

func TestEmit_NoOpWhenUnconfigured(t *testing.T) {
	// All three: nil receiver, empty gateway, empty token.
	(*httpEmitter)(nil).Emit("file.added", nil) // would panic without nil check
	newHTTPEmitter("", "tok", silentLogger{}).Emit("file.added", nil)
	newHTTPEmitter("https://example.invalid", "", silentLogger{}).Emit("file.added", nil)
	// If we got here without panic + with no goroutines hung, we're good.
}

func TestEmit_EmptyTopicDropped(t *testing.T) {
	hit := atomic.Bool{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
	}))
	defer ts.Close()
	e := newHTTPEmitter(ts.URL, "tok", silentLogger{})
	e.Emit("", nil)
	e.Emit("   ", nil)
	time.Sleep(150 * time.Millisecond)
	if hit.Load() {
		t.Fatal("empty topic should not produce a request")
	}
}

func TestEmit_ServerHangsDoesNotBlockCaller(t *testing.T) {
	// Server that responds late — well past the emitter's 100ms
	// timeout. The caller must still see Emit() return immediately
	// (fire-and-forget; the in-flight POST is its own goroutine).
	// Using a fixed sleep instead of <-r.Context().Done() so the
	// httptest server can shut down cleanly when the test ends.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	e := newHTTPEmitter(ts.URL, "tok", silentLogger{})
	start := time.Now()
	e.Emit("file.added", nil)
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("Emit blocked caller for %v (must be fire-and-forget)", elapsed)
	}
}

func TestEmit_NonJSONDataDoesNotPanic(t *testing.T) {
	// json.Marshal can't encode channels — must log + drop, not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Emit panicked on unmarshallable: %v", r)
		}
	}()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called when marshal fails")
	}))
	defer ts.Close()
	e := newHTTPEmitter(ts.URL, "tok", silentLogger{})
	e.Emit("file.added", make(chan int))
	time.Sleep(150 * time.Millisecond)
}

// waitFor polls cond every 5ms up to deadline. Returns true if cond
// became true. Used to bridge the gap between Emit's fire-and-forget
// goroutine and the test's assertion.
func waitFor(cond func() bool, deadline time.Duration) bool {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
