package sdk

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrWorkloadQueueFull = errors.New("workload queue full; retry durable work later")
var ErrNestedWorkload = errors.New("nested workload acquisition is not allowed")

type workloadKey struct{}
type workWaiter struct {
	name, project string
	at            time.Time
	ready         chan struct{}
	granted       bool
}
type WorkloadStats struct {
	Active, Queued      int
	Completed, Rejected uint64
	WaitNanoseconds     uint64
}
type Governor struct {
	mu          sync.Mutex
	policy      ResourcePolicy
	active      int
	stats       map[string]WorkloadStats
	queue       []*workWaiter
	served      map[string]float64
	lastProject map[string]string
	closed      bool
}

func NewGovernor(p ResourcePolicy) (*Governor, error) {
	if e := p.Validate(); e != nil {
		return nil, e
	}
	return &Governor{policy: cloneResourcePolicy(p), stats: map[string]WorkloadStats{}, served: map[string]float64{}, lastProject: map[string]string{}}, nil
}

// Configure never cancels active work. A reduced bound takes effect as work ends.
func (g *Governor) Configure(p ResourcePolicy) error {
	if e := p.Validate(); e != nil {
		return e
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	// Removing a declared workload while it is active would lose its accounting.
	for k := range g.policy.Workloads {
		if _, ok := p.Workloads[k]; !ok {
			return errors.New("cannot remove workload from live governor")
		}
	}
	g.policy = cloneResourcePolicy(p)
	g.dispatch()
	return nil
}
func (g *Governor) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	for _, w := range g.queue {
		close(w.ready)
	}
	g.queue = nil
	for k, s := range g.stats {
		s.Queued = 0
		g.stats[k] = s
	}
}
func (g *Governor) Snapshot() map[string]WorkloadStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]WorkloadStats{}
	for k, v := range g.stats {
		out[k] = v
	}
	return out
}
func (g *Governor) dispatch() {
	for !g.closed && (g.policy.TotalConcurrency == 0 || g.active < g.policy.TotalConcurrency) {
		best := -1
		for i, w := range g.queue {
			if g.stats[w.name].Active >= g.policy.Workloads[w.name].Concurrency {
				continue
			}
			if best < 0 {
				best = i
				continue
			}
			b := g.queue[best]
			// Aging bounds starvation even when higher-weight work keeps arriving.
			wa, ba := time.Since(w.at) >= time.Second, time.Since(b.at) >= time.Second
			if wa && (!ba || w.at.Before(b.at)) {
				best = i
				continue
			}
			if ba {
				continue
			}
			if g.served[w.name] < g.served[b.name] || (w.name == b.name && b.project == g.lastProject[b.name] && w.project != b.project) {
				best = i
			}
		}
		if best < 0 {
			return
		}
		w := g.queue[best]
		g.queue = append(g.queue[:best], g.queue[best+1:]...)
		s := g.stats[w.name]
		s.Queued--
		s.Active++
		s.WaitNanoseconds += uint64(time.Since(w.at))
		g.stats[w.name] = s
		g.active++
		g.served[w.name] += 1 / float64(g.policy.Workloads[w.name].Weight)
		g.lastProject[w.name] = w.project
		w.granted = true
		close(w.ready)
	}
}
func (g *Governor) Acquire(ctx context.Context, name, project string) (context.Context, func(), error) {
	if ctx.Value(workloadKey{}) != nil {
		return ctx, nil, ErrNestedWorkload
	}
	if e := ctx.Err(); e != nil {
		return ctx, nil, e
	}
	g.mu.Lock()
	p, ok := g.policy.Workloads[name]
	if !ok || g.closed {
		g.mu.Unlock()
		return ctx, nil, errors.New("workload unavailable")
	}
	s := g.stats[name]
	immediate := s.Queued == 0 && s.Active < p.Concurrency && (g.policy.TotalConcurrency == 0 || g.active < g.policy.TotalConcurrency)
	if !immediate && s.Queued >= p.Queue {
		s.Rejected++
		g.stats[name] = s
		g.mu.Unlock()
		return ctx, nil, ErrWorkloadQueueFull
	}
	w := &workWaiter{name: name, project: project, at: time.Now(), ready: make(chan struct{})}
	s.Queued++
	g.stats[name] = s
	g.queue = append(g.queue, w)
	g.dispatch()
	g.mu.Unlock()
	select {
	case <-w.ready:
	case <-ctx.Done():
	}
	g.mu.Lock()
	granted := w.granted
	if !granted {
		for i, q := range g.queue {
			if q == w {
				g.queue = append(g.queue[:i], g.queue[i+1:]...)
				s = g.stats[name]
				s.Queued--
				g.stats[name] = s
				break
			}
		}
	}
	g.mu.Unlock()
	if !granted {
		if e := ctx.Err(); e != nil {
			return ctx, nil, e
		}
		return ctx, nil, errors.New("workload governor closed")
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			s := g.stats[name]
			s.Active--
			s.Completed++
			g.stats[name] = s
			g.active--
			g.dispatch()
		})
	}
	if e := ctx.Err(); e != nil {
		release()
		return ctx, nil, e
	}
	return context.WithValue(ctx, workloadKey{}, g), release, nil
}

// WithWorkload is cooperative admission, never goroutine/OS priority. Do not
// hold a DB transaction while waiting. Long jobs should split into durable
// chunks and reacquire between chunks. Keep per-frame media outside this API.
func (c *AppCtx) WithWorkload(ctx context.Context, name string, fn func(context.Context) error) error {
	if c == nil || c.resources == nil || name == "" {
		return fn(ctx)
	}
	scoped, release, e := c.resources.governor.Acquire(ctx, name, c.CurrentProject())
	if e != nil {
		return e
	}
	defer release()
	return fn(scoped)
}
