package testkit

// EmitRecorder — a test-only sdk.Emitter that buffers every Emit
// call so assertions can read them back. Hand it to NewAppCtx via
// WithEmitter; use Events() / WaitForTopic() to inspect.
//
// Concurrency: protected by a mutex; the recorder can be shared
// across goroutines and is safe for tests that exercise concurrent
// handlers.

import (
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// EmittedEvent is one captured Emit() call. The data payload is
// stored as `any` since the SDK doesn't constrain it; tests cast as
// needed.
type EmittedEvent struct {
	Topic string
	Data  any
}

// EmitRecorder implements sdk.Emitter and stores every call. Get a
// new one per test via NewEmitRecorder().
type EmitRecorder struct {
	mu     sync.Mutex
	events []EmittedEvent
	signal chan struct{} // closed-and-replaced on each emit for WaitForTopic
}

// NewEmitRecorder returns a fresh recorder.
func NewEmitRecorder() *EmitRecorder {
	return &EmitRecorder{signal: make(chan struct{})}
}

// Emit captures the call. Never blocks.
func (r *EmitRecorder) Emit(topic string, data any) {
	r.mu.Lock()
	r.events = append(r.events, EmittedEvent{Topic: topic, Data: data})
	old := r.signal
	r.signal = make(chan struct{})
	r.mu.Unlock()
	close(old)
}

// Events returns a snapshot copy of every captured event so far.
// Tests can call this freely without worrying about races.
func (r *EmitRecorder) Events() []EmittedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]EmittedEvent, len(r.events))
	copy(out, r.events)
	return out
}

// EventsByTopic filters Events() to a single topic. Useful when a
// handler emits more than one and the test cares about a specific
// signal.
func (r *EmitRecorder) EventsByTopic(topic string) []EmittedEvent {
	all := r.Events()
	out := make([]EmittedEvent, 0, len(all))
	for _, ev := range all {
		if ev.Topic == topic {
			out = append(out, ev)
		}
	}
	return out
}

// WaitForTopic blocks up to deadline waiting for an event with the
// given topic. Returns (event, true) on a hit; (zero, false) on
// timeout. Useful for assertions on Emit calls that are made from a
// goroutine the test doesn't directly control (workers, async
// handlers).
func (r *EmitRecorder) WaitForTopic(topic string, deadline time.Duration) (EmittedEvent, bool) {
	end := time.Now().Add(deadline)
	for {
		// Check what we already have.
		for _, ev := range r.Events() {
			if ev.Topic == topic {
				return ev, true
			}
		}
		// Wait for the next emit or the deadline.
		r.mu.Lock()
		ch := r.signal
		r.mu.Unlock()
		remaining := time.Until(end)
		if remaining <= 0 {
			return EmittedEvent{}, false
		}
		select {
		case <-ch:
			// loop and re-check
		case <-time.After(remaining):
			return EmittedEvent{}, false
		}
	}
}

// Reset clears the captured events. Handy in table-driven tests
// that share a single ctx + recorder across cases.
func (r *EmitRecorder) Reset() {
	r.mu.Lock()
	r.events = nil
	r.mu.Unlock()
}

// Static-asserted that EmitRecorder satisfies sdk.Emitter.
var _ sdk.Emitter = (*EmitRecorder)(nil)
