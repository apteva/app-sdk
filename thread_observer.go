package sdk

// ThreadObserver answers "which threads does this agent have, how are they
// related, and what is each one doing" without every app rebuilding that from
// raw telemetry. It is an optional interface: get it with
// AppCtx.ThreadObserverAPI() or by type-asserting a TelemetryClient, and fall
// back when it is missing or a call returns ErrThreadObservationUnsupported.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"
)

// Thread states reported in ObservedThread.State.
const (
	ThreadStateWorking  = "working"  // a model turn or tool call is open
	ThreadStateSleeping = "sleeping" // a next wake is scheduled
	ThreadStateWaiting  = "waiting"  // idle until an event arrives
	ThreadStatePaused   = "paused"   // the agent is paused
	ThreadStateBlocked  = "blocked"  // model calls keep failing
	ThreadStateDone     = "done"     // ended after calling done
	ThreadStateFailed   = "failed"   // ended by a failure
	ThreadStateStopped  = "stopped"  // killed, or the agent is stopped
)

// ThreadStatusForState maps a State to the coarse original Status value.
func ThreadStatusForState(state string) string {
	switch state {
	case ThreadStateWorking:
		return "running"
	case ThreadStateSleeping, ThreadStatePaused:
		return state
	case ThreadStateWaiting, ThreadStateBlocked:
		return "waiting"
	case ThreadStateDone, ThreadStateFailed, ThreadStateStopped:
		return "stopped"
	}
	return ""
}

// ErrThreadObservationUnsupported means the server predates thread
// observation. Fall back to GetAgentTopology's id/parent tree.
var ErrThreadObservationUnsupported = errors.New("server does not support thread observation; upgrade the Server")

const threadActivityHeader = "X-Apteva-Thread-Activity"

// ThreadCurrentStep is the model turn or tool call a thread has open.
type ThreadCurrentStep struct {
	Kind  string    `json:"kind"` // model or tool
	Tool  string    `json:"tool,omitempty"`
	Since time.Time `json:"since"`
}

// ThreadSpawnLink says which thread, and which of its tool calls, started a
// thread. ToolSpanID matches ThreadStep.ToolSpanID in the parent's activity.
type ThreadSpawnLink struct {
	ThreadID    string `json:"thread_id"`
	ToolSpanID  string `json:"tool_span_id,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"`
}

// ThreadEnd describes how a thread ended. Result needs telemetry.read.
type ThreadEnd struct {
	At              time.Time `json:"at"`
	Outcome         string    `json:"outcome,omitempty"` // completed, failed or cancelled
	Result          string    `json:"result,omitempty"`
	ResultTruncated bool      `json:"result_truncated,omitempty"`
}

// AgentThreadsQuery selects an agent's threads.
type AgentThreadsQuery struct {
	ProjectID string
	AgentID   int64
	// IncludeEnded also lists threads that ended within this window (at most
	// 24 hours), so "finished two minutes ago, here's the result" still
	// shows. Needs telemetry.observe or telemetry.read; ignored otherwise.
	IncludeEnded time.Duration
}

// ThreadActivityQuery pages one thread's recent steps, newest first.
type ThreadActivityQuery struct {
	ProjectID string
	AgentID   int64
	ThreadID  string
	Cursor    string
	Limit     int // default 50, at most 200
	// Projection is metadata (default: names, status, timing) or content
	// (adds reasons, arguments, results, errors and messages; needs
	// telemetry.read for an agent the app's user owns).
	Projection string
}

// ThreadActivityPage is one page of a thread's steps.
type ThreadActivityPage struct {
	ServerID string `json:"server_id"`
	AgentID  int64  `json:"agent_id"`
	ThreadID string `json:"thread_id"`
	// PreviousIDs are earlier ids of a renamed thread; their steps are
	// included.
	PreviousIDs []string     `json:"previous_ids,omitempty"`
	Projection  string       `json:"projection"`
	Steps       []ThreadStep `json:"steps"`
	NextCursor  string       `json:"next_cursor,omitempty"`
}

// ThreadStep is one model turn, tool call, message or the end of a thread.
type ThreadStep struct {
	Kind       string    `json:"kind"` // model, tool, message or end
	ThreadID   string    `json:"thread_id"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at,omitzero"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	// Status is running, done, failed, cancelled, or unknown when a start
	// has had no end for over an hour (the call was likely lost).
	Status string `json:"status"`

	// Model steps.
	Model     string `json:"model,omitempty"`
	Provider  string `json:"provider,omitempty"`
	TokensIn  int64  `json:"tokens_in,omitempty"`
	TokensOut int64  `json:"tokens_out,omitempty"`

	// Tool steps. ChildThreadID is set when the call started a thread.
	Tool          string `json:"tool,omitempty"`
	CallID        string `json:"call_id,omitempty"`
	ToolSpanID    string `json:"tool_span_id,omitempty"`
	ChildThreadID string `json:"child_thread_id,omitempty"`

	// Message steps: the thread that received the message.
	To string `json:"to,omitempty"`

	// End steps: completed, failed or cancelled.
	Outcome string `json:"outcome,omitempty"`

	// Content projection only. Text is a message body or a thread's result.
	Reason             string `json:"reason,omitempty"`
	Arguments          string `json:"arguments,omitempty"`
	ArgumentsTruncated bool   `json:"arguments_truncated,omitempty"`
	Result             string `json:"result,omitempty"`
	ResultTruncated    bool   `json:"result_truncated,omitempty"`
	ResultBytes        int64  `json:"result_bytes,omitempty"`
	Error              string `json:"error,omitempty"`
	Text               string `json:"text,omitempty"`
	TextTruncated      bool   `json:"text_truncated,omitempty"`
}

// AgentThreadsWatch configures WatchAgentThreads.
type AgentThreadsWatch struct {
	AgentThreadsQuery
	// Refresh is how often the full list is refetched so it cannot drift.
	// Default 2 minutes with live telemetry, 15 seconds when polling.
	Refresh time.Duration
}

// AgentThreadsSnapshot is the full thread list after a change. A slow
// consumer only ever sees the latest one.
type AgentThreadsSnapshot struct {
	Threads []ObservedThread
	// Live is false while the watcher polls because live telemetry is not
	// permitted or not reachable.
	Live   bool
	Reason string // initial, event, refresh, gap or error
	Err    error  // set with Reason "error"; the previous list is kept
}

// ThreadObserver is implemented by the platform client and by
// NewTelemetryClient clients.
type ThreadObserver interface {
	// GetAgentThreads returns the topology with the extended thread fields.
	GetAgentThreads(context.Context, AgentThreadsQuery) (*AgentTopology, error)
	// GetThreadActivity returns one page of a thread's steps.
	GetThreadActivity(context.Context, ThreadActivityQuery) (*ThreadActivityPage, error)
	// WatchAgentThreads sends the full list, then a new snapshot whenever
	// telemetry changes it. The channel closes when ctx ends.
	WatchAgentThreads(context.Context, AgentThreadsWatch) (<-chan AgentThreadsSnapshot, error)
}

// ThreadObserverAPI returns the thread observer, or nil when this platform
// client has none.
func (c *AppCtx) ThreadObserverAPI() ThreadObserver {
	if c == nil {
		return nil
	}
	if p, ok := c.platform.(*projectScopedClient); ok {
		if _, ok := p.inner.(ThreadObserver); !ok {
			return nil
		}
	}
	out, _ := c.platform.(ThreadObserver)
	return out
}

func (c *telemetryClient) GetAgentThreads(ctx context.Context, q AgentThreadsQuery) (*AgentTopology, error) {
	v := telemetryQueryValues(TelemetryQuery{ProjectID: q.ProjectID})
	if q.IncludeEnded > 0 {
		v.Set("include_ended", strconv.FormatInt(int64(clampIncludeEnded(q.IncludeEnded)/time.Second), 10))
	}
	var out AgentTopology
	e := c.get(ctx, "/agents/"+strconv.FormatInt(q.AgentID, 10)+"/topology", v, &out)
	var h *telemetryHTTPError
	if errors.As(e, &h) && h.status == 404 && !h.threadActivity {
		// Servers with thread observation mark every topology response,
		// so an unmarked 404 means the route itself is missing.
		return nil, ErrThreadObservationUnsupported
	}
	if e != nil {
		return nil, e
	}
	if out.ThreadSchema < 1 {
		return nil, ErrThreadObservationUnsupported
	}
	return &out, nil
}

// maxIncludeEnded is the longest IncludeEnded window the server accepts.
const maxIncludeEnded = 24 * time.Hour

// clampIncludeEnded keeps a positive window between one second and 24 hours.
func clampIncludeEnded(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return min(max(d, time.Second), maxIncludeEnded)
}

func (c *telemetryClient) GetThreadActivity(ctx context.Context, q ThreadActivityQuery) (*ThreadActivityPage, error) {
	if q.ThreadID == "" {
		return nil, errors.New("thread id required")
	}
	var out ThreadActivityPage
	e := c.get(ctx, "/agents/"+strconv.FormatInt(q.AgentID, 10)+"/activity", threadActivityValues(q), &out)
	var h *telemetryHTTPError
	if errors.As(e, &h) && h.status == 404 && !h.threadActivity {
		return nil, ErrThreadObservationUnsupported
	}
	if e != nil {
		return nil, e
	}
	return &out, nil
}

func (c *telemetryClient) WatchAgentThreads(ctx context.Context, w AgentThreadsWatch) (<-chan AgentThreadsSnapshot, error) {
	return watchAgentThreads(ctx, c, w)
}

func (c *httpPlatformClient) GetAgentThreads(ctx context.Context, q AgentThreadsQuery) (*AgentTopology, error) {
	return c.telemetry().GetAgentThreads(ctx, q)
}
func (c *httpPlatformClient) GetThreadActivity(ctx context.Context, q ThreadActivityQuery) (*ThreadActivityPage, error) {
	return c.telemetry().GetThreadActivity(ctx, q)
}
func (c *httpPlatformClient) WatchAgentThreads(ctx context.Context, w AgentThreadsWatch) (<-chan AgentThreadsSnapshot, error) {
	return c.telemetry().WatchAgentThreads(ctx, w)
}

func (p *projectScopedClient) threadObserver(project string) (ThreadObserver, string, error) {
	q, e := p.scopedTelemetryQuery(TelemetryQuery{ProjectID: project})
	if e != nil {
		return nil, "", e
	}
	o, ok := p.inner.(ThreadObserver)
	if !ok {
		return nil, "", errors.New("thread observation unavailable")
	}
	return o, q.ProjectID, nil
}
func (p *projectScopedClient) GetAgentThreads(ctx context.Context, q AgentThreadsQuery) (*AgentTopology, error) {
	o, project, e := p.threadObserver(q.ProjectID)
	if e != nil {
		return nil, e
	}
	q.ProjectID = project
	return o.GetAgentThreads(ctx, q)
}
func (p *projectScopedClient) GetThreadActivity(ctx context.Context, q ThreadActivityQuery) (*ThreadActivityPage, error) {
	o, project, e := p.threadObserver(q.ProjectID)
	if e != nil {
		return nil, e
	}
	q.ProjectID = project
	return o.GetThreadActivity(ctx, q)
}
func (p *projectScopedClient) WatchAgentThreads(ctx context.Context, w AgentThreadsWatch) (<-chan AgentThreadsSnapshot, error) {
	o, project, e := p.threadObserver(w.ProjectID)
	if e != nil {
		return nil, e
	}
	w.ProjectID = project
	return o.WatchAgentThreads(ctx, w)
}

var _ ThreadObserver = (*telemetryClient)(nil)
var _ ThreadObserver = (*httpPlatformClient)(nil)
var _ ThreadObserver = (*projectScopedClient)(nil)

// threadWatchEvents are the telemetry types the live list applies.
var threadWatchEvents = []string{
	"thread.spawn", "thread.done", "thread.renamed", "worker.created", "worker.finished",
	"llm.start", "llm.done", "llm.error", "llm.cancelled", "tool.call", "tool.result",
	"instance.paused", "instance.resumed",
}

type threadWatchSource interface {
	GetAgentThreads(context.Context, AgentThreadsQuery) (*AgentTopology, error)
	WatchTelemetry(context.Context, TelemetryWatch) (<-chan TelemetryDelivery, error)
}

// watchAgentThreads subscribes before the first fetch so no change between
// the two is lost; replaying an event the fetch already reflects is harmless.
func watchAgentThreads(ctx context.Context, src threadWatchSource, w AgentThreadsWatch) (<-chan AgentThreadsSnapshot, error) {
	w.IncludeEnded = clampIncludeEnded(w.IncludeEnded)
	ctx, cancel := context.WithCancel(ctx)
	deliveries, watchErr := src.WatchTelemetry(ctx, TelemetryWatch{Query: TelemetryQuery{ProjectID: w.ProjectID, AgentID: w.AgentID, Events: threadWatchEvents, Projection: "metadata"}})
	live := watchErr == nil
	first, e := src.GetAgentThreads(ctx, w.AgentThreadsQuery)
	if e != nil {
		cancel()
		return nil, e
	}
	refresh := w.Refresh
	if refresh <= 0 {
		refresh = 2 * time.Minute
		if !live {
			refresh = 15 * time.Second
		}
	}
	out := make(chan AgentThreadsSnapshot, 1)
	st := newThreadWatchState(w.IncludeEnded)
	st.reset(first.Threads) // nothing earlier to keep, partial or not
	out <- AgentThreadsSnapshot{Threads: st.list(time.Now()), Live: live, Reason: "initial"}
	go func() {
		defer cancel()
		defer close(out)
		emit := func(s AgentThreadsSnapshot) {
			for {
				select {
				case out <- s:
					return
				default:
				}
				select {
				case <-out: // drop the stale snapshot the consumer has not read
				default:
				}
			}
		}
		refetch := func(reason string) {
			t, e := src.GetAgentThreads(ctx, w.AgentThreadsQuery)
			if ctx.Err() != nil {
				return
			}
			if e != nil {
				emit(AgentThreadsSnapshot{Threads: st.list(time.Now()), Live: live, Reason: "error", Err: e})
				return
			}
			if t.Partial {
				st.merge(t.Threads)
			} else {
				st.reset(t.Threads)
			}
			emit(AgentThreadsSnapshot{Threads: st.list(time.Now()), Live: live, Reason: reason})
		}
		ticker := time.NewTicker(refresh)
		defer ticker.Stop()
		var flush, soon <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refetch("refresh")
			case <-soon:
				soon = nil
				refetch("refresh")
			case <-flush:
				flush = nil
				emit(AgentThreadsSnapshot{Threads: st.list(time.Now()), Live: live, Reason: "event"})
			case d, open := <-deliveries:
				if !open || d.Kind == "error" {
					// Permanent stream failure: keep the list current by polling.
					deliveries, live = nil, false
					ticker.Reset(min(refresh, 15*time.Second))
					refetch("refresh")
					continue
				}
				switch {
				case d.Kind == "gap":
					refetch("gap")
				case d.Event != nil:
					changed, unknown := st.apply(*d.Event)
					if unknown && soon == nil {
						soon = time.After(2 * time.Second)
					}
					if changed && flush == nil {
						flush = time.After(100 * time.Millisecond)
					}
				}
			}
		}
	}()
	return out, nil
}

// threadWatchState applies telemetry to the last fetched list using the
// server's state rules. Only one goroutine touches it.
type threadWatchState struct {
	keepEnded time.Duration
	order     []string
	threads   map[string]*ObservedThread
	tools     map[string]map[string]ThreadCurrentStep // thread -> span -> open call
	models    map[string]time.Time                    // thread -> open model turn
	outcomes  map[string]string                       // worker.finished outcome before thread.done
	blocked   map[string]bool
	paused    bool
}

func newThreadWatchState(keepEnded time.Duration) *threadWatchState {
	return &threadWatchState{keepEnded: keepEnded}
}

func (s *threadWatchState) reset(threads []ObservedThread) {
	s.order = s.order[:0]
	s.threads = map[string]*ObservedThread{}
	s.tools = map[string]map[string]ThreadCurrentStep{}
	s.models = map[string]time.Time{}
	s.outcomes = map[string]string{}
	s.blocked = map[string]bool{}
	s.paused = false
	for _, t := range threads {
		t := t
		if t.Current != nil {
			t.Current = &ThreadCurrentStep{Kind: t.Current.Kind, Tool: t.Current.Tool, Since: t.Current.Since}
			if t.Current.Kind == "tool" {
				s.tools[t.ID] = map[string]ThreadCurrentStep{"snapshot:" + t.Current.Tool: *t.Current}
			} else {
				s.models[t.ID] = t.Current.Since
			}
		}
		s.paused = s.paused || t.State == ThreadStatePaused
		s.blocked[t.ID] = t.State == ThreadStateBlocked
		if _, dup := s.threads[t.ID]; !dup {
			s.order = append(s.order, t.ID)
		}
		s.threads[t.ID] = &t
	}
}

// merge applies a partial list: the server listed the threads but may have
// left out telemetry detail. Threads it lists replace earlier ones, keeping
// the earlier detail it could not fill; threads it no longer lists are
// dropped, except ended ones, which a partial list never includes.
func (s *threadWatchState) merge(threads []ObservedThread) {
	previous := s.threads
	keepTools, keepModels := s.tools, s.models
	var ended []ObservedThread
	listed := map[string]bool{}
	for _, t := range threads {
		listed[t.ID] = true
	}
	for _, id := range s.order {
		if t, ok := previous[id]; ok && t.Ended != nil && !listed[id] {
			ended = append(ended, *t)
		}
	}
	merged := make([]ObservedThread, 0, len(threads)+len(ended))
	for _, t := range threads {
		if old, ok := previous[t.ID]; ok && old.Ended == nil {
			if t.Current == nil && old.Current != nil {
				t.Current = old.Current
				if t.State != ThreadStatePaused && t.State != ThreadStateBlocked {
					t.State = ThreadStateWorking
					t.Status = ThreadStatusForState(t.State)
				}
			}
			if t.LastActivityAt.IsZero() {
				t.LastActivityAt = old.LastActivityAt
			}
			if t.SpawnedBy == nil {
				t.SpawnedBy = old.SpawnedBy
			}
			if t.StartedAt.IsZero() {
				t.StartedAt = old.StartedAt
			}
			if t.Purpose == "" {
				t.Purpose = old.Purpose
			}
		}
		merged = append(merged, t)
	}
	merged = append(merged, ended...)
	s.reset(merged)
	// Open calls known only from the stream survive a partial refetch.
	for id, calls := range keepTools {
		if _, ok := s.threads[id]; ok && len(calls) > 0 {
			s.tools[id] = calls
		}
	}
	for id, since := range keepModels {
		if _, ok := s.threads[id]; ok {
			if _, has := s.models[id]; !has {
				s.models[id] = since
			}
		}
	}
}

func (s *threadWatchState) list(now time.Time) []ObservedThread {
	out := make([]ObservedThread, 0, len(s.threads))
	kept := s.order[:0]
	for _, id := range s.order {
		t, ok := s.threads[id]
		if !ok {
			continue
		}
		if t.Ended != nil && (s.keepEnded <= 0 || now.Sub(t.Ended.At) > s.keepEnded) {
			delete(s.threads, id)
			continue
		}
		kept = append(kept, id)
		copied := *t
		if t.Current != nil {
			current := *t.Current
			copied.Current = &current
		}
		if t.SpawnedBy != nil {
			link := *t.SpawnedBy
			copied.SpawnedBy = &link
		}
		if t.Ended != nil {
			end := *t.Ended
			copied.Ended = &end
		}
		out = append(out, copied)
	}
	s.order = kept
	return out
}

// apply reports whether the list changed, and whether the event named a
// thread the list does not know (the caller then refetches).
func (s *threadWatchState) apply(ev TelemetryStreamEvent) (changed, unknown bool) {
	id := ev.ThreadID
	if id == "" {
		id = "main"
	}
	data := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(ev.Data))
	dec.UseNumber()
	_ = dec.Decode(&data)
	str := func(key string) string {
		v, _ := data[key].(string)
		return v
	}
	switch ev.Type {
	case "instance.paused", "instance.resumed":
		s.paused = ev.Type == "instance.paused"
		for tid := range s.threads {
			s.recompute(tid, ev.Time)
		}
		return true, false
	case "thread.spawn", "worker.created":
		t, ok := s.threads[id]
		if !ok || t.Ended != nil {
			// A new thread, or a new one reusing an ended thread's id (which
			// keeps its place in the order).
			if !ok {
				s.order = append(s.order, id)
			}
			t = &ObservedThread{ID: id, ParentID: "main", StartedAt: ev.Time}
			s.threads[id] = t
			delete(s.tools, id)
			delete(s.models, id)
			delete(s.outcomes, id)
			s.blocked[id] = false
			unknown = true // fetch name, model and purpose
		}
		if parent := str("parent_id"); parent != "" {
			t.ParentID = parent
		}
		if parent := str("parent_thread_id"); parent != "" {
			t.ParentID = parent
		}
		if span, execution := str("parent_tool_span_id"), str("parent_execution_id"); span != "" || execution != "" || ev.Type == "worker.created" {
			if t.SpawnedBy == nil {
				t.SpawnedBy = &ThreadSpawnLink{}
			}
			t.SpawnedBy.ThreadID = t.ParentID
			if span != "" {
				t.SpawnedBy.ToolSpanID = span
			}
			if execution != "" {
				t.SpawnedBy.ExecutionID = execution
			}
		}
		if p, ok := s.threads[t.ParentID]; ok && t.ParentID != t.ID {
			t.Depth = p.Depth + 1
		}
		if t.StartedAt.IsZero() {
			t.StartedAt = ev.Time
		}
	case "thread.renamed":
		oldID, newID := str("old_id"), str("new_id")
		t, ok := s.threads[oldID]
		if !ok || newID == "" {
			return false, true
		}
		if oldID != newID {
			delete(s.threads, oldID)
			t.ID = newID
			s.threads[newID] = t
			for i, o := range s.order {
				if o == oldID {
					s.order[i] = newID
				}
			}
			for _, child := range s.threads {
				if child.ParentID == oldID {
					child.ParentID = newID
				}
			}
			// Move only what exists: a missing open-turn entry must not become
			// a zero-time open turn under the new id.
			if v, ok := s.tools[oldID]; ok {
				s.tools[newID] = v
			}
			if v, ok := s.models[oldID]; ok {
				s.models[newID] = v
			}
			if v, ok := s.blocked[oldID]; ok {
				s.blocked[newID] = v
			}
			delete(s.tools, oldID)
			delete(s.models, oldID)
			delete(s.blocked, oldID)
		}
		return true, true // the new name comes with the refetch
	case "worker.finished":
		if outcome := str("outcome"); outcome != "" {
			s.outcomes[id] = outcome
			if t, ok := s.threads[id]; ok && t.Ended != nil && t.Ended.Outcome == "" {
				t.Ended.Outcome = outcome
				t.State = threadStateForOutcome(outcome)
				t.Status = ThreadStatusForState(t.State)
				return true, false
			}
		}
		return false, false
	case "thread.done":
		t, ok := s.threads[id]
		if !ok {
			return false, false
		}
		outcome := str("outcome")
		if outcome == "" {
			outcome = s.outcomes[id]
		}
		t.Ended = &ThreadEnd{At: ev.Time, Outcome: outcome}
		t.Current = nil
		t.State = threadStateForOutcome(outcome)
		t.Status = ThreadStatusForState(t.State)
		t.NextWakeAt = time.Time{}
		delete(s.tools, id)
		delete(s.models, id)
		if ev.Time.After(t.LastActivityAt) {
			t.LastActivityAt = ev.Time
		}
		return true, false
	case "llm.start":
		if _, ok := s.models[id]; !ok {
			s.models[id] = ev.Time
		}
	case "llm.done", "llm.error", "llm.cancelled":
		delete(s.models, id)
		if ev.Type == "llm.done" {
			s.blocked[id] = false
		}
	case "tool.call":
		span := toolSpanKey(data)
		if s.tools[id] == nil {
			s.tools[id] = map[string]ThreadCurrentStep{}
		}
		s.tools[id][span] = ThreadCurrentStep{Kind: "tool", Tool: str("tool_name"), Since: ev.Time}
	case "tool.result":
		open := s.tools[id]
		span := toolSpanKey(data)
		if _, ok := open[span]; ok {
			delete(open, span)
		} else {
			// Snapshot entries have no span; close the oldest call of that tool.
			var oldest string
			for key, step := range open {
				if step.Tool == str("tool_name") && (oldest == "" || step.Since.Before(open[oldest].Since)) {
					oldest = key
				}
			}
			delete(open, oldest)
		}
	default:
		return false, false
	}
	t, ok := s.threads[id]
	if !ok {
		return false, true
	}
	if t.Ended != nil {
		return false, false
	}
	if ev.Time.After(t.LastActivityAt) {
		t.LastActivityAt = ev.Time
	}
	s.recompute(id, ev.Time)
	return true, unknown
}

func toolSpanKey(data map[string]any) string {
	for _, key := range []string{"tool_span_id", "call_id", "tool_call_id"} {
		if v, _ := data[key].(string); v != "" {
			return v
		}
	}
	return ""
}

func (s *threadWatchState) recompute(id string, now time.Time) {
	t, ok := s.threads[id]
	if !ok || t.Ended != nil {
		return
	}
	var current *ThreadCurrentStep
	if since, ok := s.models[id]; ok {
		current = &ThreadCurrentStep{Kind: "model", Since: since}
	}
	for _, step := range s.tools[id] {
		if current == nil || step.Since.After(current.Since) {
			step := step
			current = &step
		}
	}
	t.Current = current
	switch {
	case s.paused:
		t.State = ThreadStatePaused
	case s.blocked[id]:
		t.State = ThreadStateBlocked
	case current != nil:
		t.State = ThreadStateWorking
	case t.NextWakeAt.After(now):
		t.State = ThreadStateSleeping
	default:
		t.State = ThreadStateWaiting
	}
	t.Status = ThreadStatusForState(t.State)
}

func threadStateForOutcome(outcome string) string {
	switch outcome {
	case "failed":
		return ThreadStateFailed
	case "cancelled":
		return ThreadStateStopped
	}
	return ThreadStateDone
}

func threadActivityValues(q ThreadActivityQuery) url.Values {
	v := telemetryQueryValues(TelemetryQuery{ProjectID: q.ProjectID, ThreadID: q.ThreadID, Cursor: q.Cursor, Projection: q.Projection})
	if q.Limit != 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	return v
}
