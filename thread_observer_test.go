package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetAgentThreadsSendsWindowAndRejectsOldServers(t *testing.T) {
	schema := 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/telemetry/agents/7/topology" || r.URL.Query().Get("project_id") != "p" {
			t.Error("topology route", r.URL.String())
		}
		if got := r.URL.Query().Get("include_ended"); got != "120" {
			t.Error("include_ended", got)
		}
		json.NewEncoder(w).Encode(AgentTopology{AgentID: 7, ThreadSchema: schema, Threads: []ObservedThread{{ID: "main", State: ThreadStateWaiting}}})
	}))
	defer server.Close()
	client, _ := NewTelemetryClient(server.URL, "key")
	observer := client.(ThreadObserver)
	got, e := observer.GetAgentThreads(context.Background(), AgentThreadsQuery{ProjectID: "p", AgentID: 7, IncludeEnded: 2 * time.Minute})
	if e != nil || got.Threads[0].State != ThreadStateWaiting {
		t.Fatal(got, e)
	}
	schema = 0
	if _, e = observer.GetAgentThreads(context.Background(), AgentThreadsQuery{ProjectID: "p", AgentID: 7, IncludeEnded: 2 * time.Minute}); !errors.Is(e, ErrThreadObservationUnsupported) {
		t.Fatal("old server accepted", e)
	}
}

func TestGetThreadActivityTellsOldServersFromMissingAgents(t *testing.T) {
	marked := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/telemetry/agents/7/activity" {
			t.Error("activity route", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("thread_id") != "worker" || q.Get("cursor") != "c1" || q.Get("limit") != "25" || q.Get("projection") != "content" {
			t.Error("activity query", r.URL.RawQuery)
		}
		if marked {
			w.Header().Set(threadActivityHeader, "1")
		}
		if q.Get("project_id") == "missing" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(ThreadActivityPage{ThreadID: "worker", Steps: []ThreadStep{{Kind: "tool", Tool: "crm_list", Status: "done"}}})
	}))
	defer server.Close()
	client, _ := NewTelemetryClient(server.URL, "key")
	observer := client.(ThreadObserver)
	q := ThreadActivityQuery{ProjectID: "p", AgentID: 7, ThreadID: "worker", Cursor: "c1", Limit: 25, Projection: "content"}
	page, e := observer.GetThreadActivity(context.Background(), q)
	if e != nil || len(page.Steps) != 1 || page.Steps[0].Tool != "crm_list" {
		t.Fatal(page, e)
	}
	q.ProjectID = "missing"
	if _, e = observer.GetThreadActivity(context.Background(), q); e == nil || errors.Is(e, ErrThreadObservationUnsupported) {
		t.Fatal("a missing agent on a new server is not an old server", e)
	}
	marked = false
	if _, e = observer.GetThreadActivity(context.Background(), q); !errors.Is(e, ErrThreadObservationUnsupported) {
		t.Fatal("old server not detected", e)
	}
	if _, e = observer.GetThreadActivity(context.Background(), ThreadActivityQuery{AgentID: 7}); e == nil {
		t.Fatal("empty thread id accepted")
	}
}

func TestThreadObserverAPIKeepsProjectScope(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/apps/callback/telemetry/agents/7/topology" || r.URL.Query().Get("project_id") != "p" {
			t.Error("callback route", r.URL.String())
		}
		json.NewEncoder(w).Encode(AgentTopology{ThreadSchema: 1})
	}))
	defer server.Close()
	app := NewAppCtxForTest(nil, nil, nil, &projectScopedClient{inner: newHTTPPlatformClient(server.URL, "key"), projectID: "p"}, nil)
	observer := app.ThreadObserverAPI()
	if observer == nil {
		t.Fatal("observer unavailable")
	}
	if _, e := observer.GetAgentThreads(context.Background(), AgentThreadsQuery{AgentID: 7}); e != nil {
		t.Fatal(e)
	}
	if _, e := observer.GetAgentThreads(context.Background(), AgentThreadsQuery{ProjectID: "other", AgentID: 7}); e == nil || calls.Load() != 1 {
		t.Fatal("project override reached network", e)
	}
	if (&AppCtx{platform: &projectScopedClient{inner: struct{ PlatformClient }{}}}).ThreadObserverAPI() != nil {
		t.Fatal("observer claimed for a client without one")
	}
}

// fakeThreadServer serves a topology the test edits and a delivery stream the
// test feeds.
type fakeThreadServer struct {
	t        *testing.T
	mu       sync.Mutex
	threads  []ObservedThread
	fetches  atomic.Int32
	streams  atomic.Int32
	refuse   bool
	frames   chan TelemetryDelivery
	reconnct chan struct{} // drops the current stream connection
}

func newFakeThreadServer(t *testing.T, threads ...ObservedThread) (*fakeThreadServer, *httptest.Server) {
	f := &fakeThreadServer{t: t, threads: threads, frames: make(chan TelemetryDelivery, 32), reconnct: make(chan struct{}, 1)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/telemetry/agents/7/topology":
			f.fetches.Add(1)
			f.mu.Lock()
			out := AgentTopology{AgentID: 7, ThreadSchema: 1, Threads: append([]ObservedThread(nil), f.threads...)}
			f.mu.Unlock()
			json.NewEncoder(w).Encode(out)
		case "/api/telemetry/stream":
			f.streams.Add(1)
			if f.refuse {
				http.Error(w, "missing telemetry observation/content permission", 403)
				return
			}
			q := r.URL.Query()
			if q.Get("agent_id") != "7" || q.Get("projection") != "metadata" || q.Get("format") != "delivery" {
				t.Error("watch query", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-f.reconnct:
					return // drop the connection
				case d := <-f.frames:
					raw, _ := json.Marshal(d)
					fmt.Fprintf(w, "data: %s\n\n", raw)
					w.(http.Flusher).Flush()
				}
			}
		default:
			t.Error("unexpected route", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	return f, server
}

func (f *fakeThreadServer) set(threads ...ObservedThread) {
	f.mu.Lock()
	f.threads = threads
	f.mu.Unlock()
}

var threadEventSeq atomic.Int64

func (f *fakeThreadServer) send(thread, kind string, at time.Time, data map[string]any) {
	raw, _ := json.Marshal(data)
	n := threadEventSeq.Add(1)
	f.frames <- TelemetryDelivery{Kind: "event", ServerID: "s", Cursor: fmt.Sprint(n), Event: &TelemetryStreamEvent{ServerID: "s", ID: fmt.Sprint("ev", n), AgentID: 7, ThreadID: thread, Type: kind, Time: at, Data: raw}}
}

// waitForThreads reads snapshots until one satisfies ok.
func waitForThreads(t *testing.T, ch <-chan AgentThreadsSnapshot, what string, ok func(AgentThreadsSnapshot) bool) AgentThreadsSnapshot {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case s, open := <-ch:
			if !open {
				t.Fatalf("%s: watch closed", what)
			}
			if ok(s) {
				return s
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func threadByID(s AgentThreadsSnapshot, id string) *ObservedThread {
	for i := range s.Threads {
		if s.Threads[i].ID == id {
			return &s.Threads[i]
		}
	}
	return nil
}

func TestWatchAgentThreadsFollowsSpawnWorkAndEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	f, server := newFakeThreadServer(t, ObservedThread{ID: "main", State: ThreadStateWaiting, Status: "waiting"})
	defer server.Close()
	defer cancel() // before Close, which waits for the open stream
	client, _ := NewTelemetryClient(server.URL, "key")
	ch, e := client.(ThreadObserver).WatchAgentThreads(ctx, AgentThreadsWatch{AgentThreadsQuery: AgentThreadsQuery{AgentID: 7, IncludeEnded: time.Hour}})
	if e != nil {
		t.Fatal(e)
	}
	first := <-ch
	if first.Reason != "initial" || !first.Live || len(first.Threads) != 1 {
		t.Fatalf("initial snapshot %+v", first)
	}

	// The refetch the spawn triggers must not undo the live state below.
	f.set(ObservedThread{ID: "main", State: ThreadStateWaiting}, ObservedThread{ID: "worker", ParentID: "main", Name: "Inbox", Depth: 1, State: ThreadStateWaiting})
	f.send("worker", "worker.created", now, map[string]any{"parent_thread_id": "main", "parent_tool_span_id": "span-spawn"})
	f.send("worker", "thread.spawn", now, map[string]any{"parent_id": "main", "parent_execution_id": "exe-1"})
	s := waitForThreads(t, ch, "spawned worker", func(s AgentThreadsSnapshot) bool {
		w := threadByID(s, "worker")
		return w != nil && w.SpawnedBy != nil && w.SpawnedBy.ExecutionID != ""
	})
	w := threadByID(s, "worker")
	if w.ParentID != "main" || w.Depth != 1 || w.SpawnedBy == nil || w.SpawnedBy.ToolSpanID != "span-spawn" || w.SpawnedBy.ExecutionID != "exe-1" {
		t.Fatalf("spawned worker %+v %+v", w, w.SpawnedBy)
	}
	waitForThreads(t, ch, "name from refetch", func(s AgentThreadsSnapshot) bool {
		w := threadByID(s, "worker")
		return w != nil && w.Name == "Inbox"
	})

	f.send("worker", "tool.call", now.Add(time.Second), map[string]any{"tool_name": "crm_list", "tool_span_id": "span-1"})
	s = waitForThreads(t, ch, "open tool", func(s AgentThreadsSnapshot) bool {
		w := threadByID(s, "worker")
		return w != nil && w.State == ThreadStateWorking
	})
	if w = threadByID(s, "worker"); w.Current == nil || w.Current.Kind != "tool" || w.Current.Tool != "crm_list" || w.Status != "running" || !w.LastActivityAt.Equal(now.Add(time.Second)) {
		t.Fatalf("working worker %+v %+v", w, w.Current)
	}
	f.send("worker", "tool.result", now.Add(2*time.Second), map[string]any{"tool_name": "crm_list", "tool_span_id": "span-1", "success": true})
	waitForThreads(t, ch, "closed tool", func(s AgentThreadsSnapshot) bool {
		w := threadByID(s, "worker")
		return w != nil && w.State == ThreadStateWaiting && w.Current == nil
	})

	f.send("worker", "llm.start", now.Add(3*time.Second), nil)
	waitForThreads(t, ch, "model turn", func(s AgentThreadsSnapshot) bool {
		w := threadByID(s, "worker")
		return w != nil && w.Current != nil && w.Current.Kind == "model"
	})
	f.send("worker", "llm.done", now.Add(4*time.Second), nil)
	f.send("worker", "worker.finished", now.Add(4*time.Second), map[string]any{"outcome": "completed"})
	f.send("worker", "thread.done", now.Add(4*time.Second), map[string]any{"parent_id": "main"})
	s = waitForThreads(t, ch, "ended worker", func(s AgentThreadsSnapshot) bool {
		w := threadByID(s, "worker")
		return w != nil && w.Ended != nil
	})
	if w = threadByID(s, "worker"); w.State != ThreadStateDone || w.Ended.Outcome != "completed" || w.Current != nil || w.Status != "stopped" {
		t.Fatalf("ended worker %+v %+v", w, w.Ended)
	}
}

func TestWatchAgentThreadsRenamesAndDropsEndedWithoutWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	f, server := newFakeThreadServer(t,
		ObservedThread{ID: "main"},
		ObservedThread{ID: "old", ParentID: "main", Depth: 1, State: ThreadStateWaiting},
		ObservedThread{ID: "child", ParentID: "old", Depth: 2, State: ThreadStateWaiting})
	defer server.Close()
	defer cancel() // before Close, which waits for the open stream
	client, _ := NewTelemetryClient(server.URL, "key")
	ch, e := client.(ThreadObserver).WatchAgentThreads(ctx, AgentThreadsWatch{AgentThreadsQuery: AgentThreadsQuery{AgentID: 7}})
	if e != nil {
		t.Fatal(e)
	}
	<-ch
	f.set(ObservedThread{ID: "main"}, ObservedThread{ID: "new", ParentID: "main", Depth: 1, Name: "Renamed"}, ObservedThread{ID: "child", ParentID: "new", Depth: 2})
	f.send("new", "thread.renamed", now, map[string]any{"old_id": "old", "new_id": "new"})
	s := waitForThreads(t, ch, "rename", func(s AgentThreadsSnapshot) bool { return threadByID(s, "new") != nil })
	if threadByID(s, "old") != nil || threadByID(s, "child").ParentID != "new" {
		t.Fatalf("rename did not re-key: %+v", s.Threads)
	}
	f.set(ObservedThread{ID: "main"}, ObservedThread{ID: "new", ParentID: "main", Depth: 1, Name: "Renamed"})
	f.send("child", "thread.done", now.Add(time.Second), map[string]any{"parent_id": "new", "outcome": "cancelled"})
	waitForThreads(t, ch, "ended child dropped", func(s AgentThreadsSnapshot) bool { return threadByID(s, "child") == nil && len(s.Threads) == 2 })
}

func TestWatchAgentThreadsRefetchesAfterGapAndOnSchedule(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f, server := newFakeThreadServer(t, ObservedThread{ID: "main", State: ThreadStateWaiting})
	defer server.Close()
	defer cancel() // before Close, which waits for the open stream
	client, _ := NewTelemetryClient(server.URL, "key")
	ch, e := client.(ThreadObserver).WatchAgentThreads(ctx, AgentThreadsWatch{AgentThreadsQuery: AgentThreadsQuery{AgentID: 7}, Refresh: 300 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	<-ch
	// A change telemetry never reported (e.g. a missed event) is repaired by
	// the periodic refetch.
	f.set(ObservedThread{ID: "main", State: ThreadStateSleeping, NextWakeAt: time.Now().Add(time.Hour)})
	waitForThreads(t, ch, "scheduled refetch", func(s AgentThreadsSnapshot) bool {
		return s.Reason == "refresh" && s.Threads[0].State == ThreadStateSleeping
	})
	f.set(ObservedThread{ID: "main", State: ThreadStatePaused})
	f.frames <- TelemetryDelivery{Kind: "gap", ServerID: "s", Cursor: "epoch-2", Reason: "server_restarted"}
	waitForThreads(t, ch, "gap refetch", func(s AgentThreadsSnapshot) bool {
		return s.Reason == "gap" && s.Threads[0].State == ThreadStatePaused
	})
	// A dropped connection reconnects without losing the live list.
	streams := f.streams.Load()
	f.reconnct <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for f.streams.Load() == streams && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	f.set(ObservedThread{ID: "main", State: ThreadStateWaiting})
	f.send("main", "instance.resumed", time.Now(), nil)
	waitForThreads(t, ch, "event after reconnect", func(s AgentThreadsSnapshot) bool {
		return s.Reason == "event" && s.Threads[0].State == ThreadStateWaiting
	})
}

func TestWatchAgentThreadsPollsWithoutTelemetryPermission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f, server := newFakeThreadServer(t, ObservedThread{ID: "main", State: ThreadStateWaiting})
	f.refuse = true
	defer server.Close()
	defer cancel() // before Close, which waits for the open stream
	client, _ := NewTelemetryClient(server.URL, "key")
	ch, e := client.(ThreadObserver).WatchAgentThreads(ctx, AgentThreadsWatch{AgentThreadsQuery: AgentThreadsQuery{AgentID: 7}, Refresh: 200 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	if first := <-ch; first.Live {
		t.Fatal("refused stream reported live")
	}
	f.set(ObservedThread{ID: "main", State: ThreadStateWaiting}, ObservedThread{ID: "worker", ParentID: "main", State: ThreadStateWaiting})
	s := waitForThreads(t, ch, "polled change", func(s AgentThreadsSnapshot) bool { return threadByID(s, "worker") != nil })
	if s.Live {
		t.Fatal("poll snapshot reported live")
	}
}

func TestWatchAgentThreadsRejectsOldServers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/telemetry/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		json.NewEncoder(w).Encode(AgentTopology{Threads: []ObservedThread{{ID: "main"}}})
	}))
	defer server.Close()
	client, _ := NewTelemetryClient(server.URL, "key")
	if _, e := client.(ThreadObserver).WatchAgentThreads(context.Background(), AgentThreadsWatch{AgentThreadsQuery: AgentThreadsQuery{AgentID: 7}}); !errors.Is(e, ErrThreadObservationUnsupported) {
		t.Fatal("old server accepted", e)
	}
}

func TestThreadWatchStateClosesSnapshotToolByName(t *testing.T) {
	st := newThreadWatchState(0)
	since := time.Now().Add(-time.Minute)
	st.reset([]ObservedThread{{ID: "w", State: ThreadStateWorking, Current: &ThreadCurrentStep{Kind: "tool", Tool: "crm_list", Since: since}}})
	raw, _ := json.Marshal(map[string]any{"tool_name": "crm_list", "tool_span_id": "span-unknown"})
	if changed, _ := st.apply(TelemetryStreamEvent{ThreadID: "w", Type: "tool.result", Time: time.Now(), Data: raw}); !changed {
		t.Fatal("result ignored")
	}
	if w := st.list(time.Now())[0]; w.State != ThreadStateWaiting || w.Current != nil {
		t.Fatalf("snapshot call stayed open: %+v", w)
	}
}

func TestThreadStatusForState(t *testing.T) {
	for state, want := range map[string]string{
		ThreadStateWorking: "running", ThreadStateSleeping: "sleeping", ThreadStateWaiting: "waiting",
		ThreadStatePaused: "paused", ThreadStateBlocked: "waiting", ThreadStateDone: "stopped",
		ThreadStateFailed: "stopped", ThreadStateStopped: "stopped", "": "",
	} {
		if got := ThreadStatusForState(state); got != want {
			t.Errorf("%q -> %q, want %q", state, got, want)
		}
	}
}

// Review 2026-11-25: a rename copied empty open-turn entries to the new id, so
// the next event reported the thread working since year 1.
func TestThreadWatchStateRenameWithoutOpenTurnStaysIdle(t *testing.T) {
	st := newThreadWatchState(0)
	st.reset([]ObservedThread{{ID: "main"}, {ID: "old", ParentID: "main", State: ThreadStateWaiting}})
	now := time.Now().UTC()
	event := func(thread, kind string, data map[string]any) {
		raw, _ := json.Marshal(data)
		st.apply(TelemetryStreamEvent{ThreadID: thread, Type: kind, Time: now, Data: raw})
	}
	event("new", "thread.renamed", map[string]any{"old_id": "old", "new_id": "new"})
	event("new", "tool.call", map[string]any{"tool_name": "crm_list", "tool_span_id": "s1"})
	event("new", "tool.result", map[string]any{"tool_name": "crm_list", "tool_span_id": "s1"})
	for _, th := range st.list(now) {
		if th.ID != "new" {
			continue
		}
		if th.State != ThreadStateWaiting || th.Current != nil {
			t.Fatalf("renamed thread state=%q current=%+v, want waiting with nothing open", th.State, th.Current)
		}
		return
	}
	t.Fatal("renamed thread missing")
}

// An open turn does move with the rename.
func TestThreadWatchStateRenameKeepsOpenTurn(t *testing.T) {
	st := newThreadWatchState(0)
	since := time.Now().Add(-time.Minute).UTC()
	st.reset([]ObservedThread{{ID: "old", State: ThreadStateWorking, Current: &ThreadCurrentStep{Kind: "model", Since: since}}})
	raw, _ := json.Marshal(map[string]any{"old_id": "old", "new_id": "new"})
	st.apply(TelemetryStreamEvent{ThreadID: "new", Type: "thread.renamed", Time: time.Now(), Data: raw})
	raw, _ = json.Marshal(map[string]any{"tool_name": "crm_list", "tool_span_id": "s1"})
	st.apply(TelemetryStreamEvent{ThreadID: "new", Type: "tool.result", Time: time.Now(), Data: raw})
	th := st.list(time.Now())[0]
	if th.ID != "new" || th.State != ThreadStateWorking || th.Current == nil || !th.Current.Since.Equal(since) {
		t.Fatalf("open turn lost in rename: %+v", th)
	}
}

func TestGetAgentThreadsClampsWindowAndSpotsMissingRoute(t *testing.T) {
	var windows []string
	marked := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if marked {
			w.Header().Set(threadActivityHeader, "1")
		}
		if r.URL.Query().Get("project_id") == "missing" {
			http.NotFound(w, r)
			return
		}
		windows = append(windows, r.URL.Query().Get("include_ended"))
		json.NewEncoder(w).Encode(AgentTopology{AgentID: 7, ThreadSchema: 1})
	}))
	defer server.Close()
	client, _ := NewTelemetryClient(server.URL, "key")
	observer := client.(ThreadObserver)
	for _, d := range []time.Duration{48 * time.Hour, 200 * time.Millisecond} {
		if _, e := observer.GetAgentThreads(context.Background(), AgentThreadsQuery{ProjectID: "p", AgentID: 7, IncludeEnded: d}); e != nil {
			t.Fatal(e)
		}
	}
	if len(windows) != 2 || windows[0] != "86400" || windows[1] != "1" {
		t.Fatalf("include_ended sent %v, want [86400 1]", windows)
	}
	// A marked 404 is a missing agent, not an old server.
	_, e := observer.GetAgentThreads(context.Background(), AgentThreadsQuery{ProjectID: "missing", AgentID: 7})
	if e == nil || errors.Is(e, ErrThreadObservationUnsupported) {
		t.Fatalf("missing agent reported as %v", e)
	}
	marked = false
	if _, e = observer.GetAgentThreads(context.Background(), AgentThreadsQuery{ProjectID: "missing", AgentID: 7}); !errors.Is(e, ErrThreadObservationUnsupported) {
		t.Fatalf("server without the route reported as %v", e)
	}
}

// A partial list (the server ran out of time adding detail) keeps the detail
// the watcher already had instead of wiping it.
func TestThreadWatchStateMergeKeepsDetailFromPartialList(t *testing.T) {
	st := newThreadWatchState(time.Hour)
	now := time.Now().UTC()
	since := now.Add(-30 * time.Second)
	ended := now.Add(-time.Minute)
	st.reset([]ObservedThread{
		{ID: "main", State: ThreadStateWaiting},
		{ID: "w", ParentID: "main", State: ThreadStateWorking, Current: &ThreadCurrentStep{Kind: "tool", Tool: "crm_list", Since: since},
			LastActivityAt: since, SpawnedBy: &ThreadSpawnLink{ThreadID: "main", ToolSpanID: "span-1"}, Purpose: "Check the inbox"},
		{ID: "gone", ParentID: "main", State: ThreadStateDone, Ended: &ThreadEnd{At: ended, Outcome: "completed"}},
	})
	st.merge([]ObservedThread{{ID: "main", State: ThreadStateWaiting}, {ID: "w", ParentID: "main", State: ThreadStateWaiting}})
	list := st.list(now)
	byID := map[string]ObservedThread{}
	for _, th := range list {
		byID[th.ID] = th
	}
	w := byID["w"]
	if w.State != ThreadStateWorking || w.Current == nil || w.Current.Tool != "crm_list" || w.SpawnedBy == nil || w.SpawnedBy.ToolSpanID != "span-1" ||
		!w.LastActivityAt.Equal(since) || w.Purpose != "Check the inbox" {
		t.Fatalf("partial list wiped detail: %+v", w)
	}
	if g, ok := byID["gone"]; !ok || g.Ended == nil {
		t.Fatalf("partial list dropped an ended thread: %+v", list)
	}
	// The open call still closes on its result.
	raw, _ := json.Marshal(map[string]any{"tool_name": "crm_list", "tool_span_id": "x"})
	st.apply(TelemetryStreamEvent{ThreadID: "w", Type: "tool.result", Time: now, Data: raw})
	for _, th := range st.list(now) {
		if th.ID == "w" && (th.State != ThreadStateWaiting || th.Current != nil) {
			t.Fatalf("kept call never closed: %+v", th)
		}
	}
}

// A full list after a partial one replaces everything again.
func TestWatchAgentThreadsUsesPartialListWithoutWipingDetail(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	since := time.Now().Add(-time.Minute).UTC()
	var partial atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/telemetry/stream" {
			http.Error(w, "missing telemetry observation/content permission", 403)
			return
		}
		out := AgentTopology{AgentID: 7, ThreadSchema: 1, Threads: []ObservedThread{{ID: "main", State: ThreadStateWorking, Current: &ThreadCurrentStep{Kind: "model", Since: since}}}}
		if partial.Load() {
			out = AgentTopology{AgentID: 7, ThreadSchema: 1, Partial: true, Threads: []ObservedThread{{ID: "main", State: ThreadStateWaiting}}}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer server.Close()
	defer cancel()
	client, _ := NewTelemetryClient(server.URL, "key")
	ch, e := client.(ThreadObserver).WatchAgentThreads(ctx, AgentThreadsWatch{AgentThreadsQuery: AgentThreadsQuery{AgentID: 7}, Refresh: 50 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	<-ch
	partial.Store(true)
	s := waitForThreads(t, ch, "partial refresh", func(s AgentThreadsSnapshot) bool { return s.Reason == "refresh" })
	if m := threadByID(s, "main"); m == nil || m.State != ThreadStateWorking || m.Current == nil {
		t.Fatalf("partial refresh wiped the open turn: %+v", s.Threads)
	}
}
