package sdk

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func resourceTestDeclaration() *ResourceDeclaration {
	return &ResourceDeclaration{Workloads: []string{"interactive", "batch"}, TrafficClasses: []string{"media"}, Defaults: ResourcePolicy{TotalConcurrency: 1, Workloads: map[string]WorkloadPolicy{"interactive": {1, 4, 4}, "batch": {1, 4, 1}}, Network: map[string]SocketPolicy{"media": {}}}}
}
func TestResourceResolutionBoundsAndIsolation(t *testing.T) {
	d := resourceTestDeclaration()
	badDSCP := 64
	for _, p := range []ResourcePolicy{{Network: map[string]SocketPolicy{"media": {DSCP: &badDSCP}}}, {Workloads: map[string]WorkloadPolicy{"surprise": {1, 1, 1}}}, {Process: ProcessResources{MemoryHighBytes: 100, MemoryMaxBytes: 50}}, {Network: map[string]SocketPolicy{"media": {ReceiveBytes: 5 << 20}}}} {
		if _, e := ResolveResources(d, &p); e == nil {
			t.Fatalf("accepted %#v", p)
		}
	}
	p, e := ResolveResources(d, &ResourcePolicy{Workloads: map[string]WorkloadPolicy{"batch": {2, 3, 2}}})
	if e != nil {
		t.Fatal(e)
	}
	if d.Defaults.Workloads["batch"].Concurrency != 1 || p.Workloads["batch"].Concurrency != 2 {
		t.Fatal("resolution mutated default")
	}
	props, e := (ProcessResources{CPUWeight: 200, CPUQuotaPercent: 150, MemoryMaxBytes: 1000000}).SystemdProperties()
	if e != nil || !strings.Contains(strings.Join(props, "\n"), "CPUQuota=150%") {
		t.Fatal(props, e)
	}
}
func TestGovernorBoundsCancellationNestedAndRecovery(t *testing.T) {
	p := resourceTestDeclaration().Defaults
	g, _ := NewGovernor(p)
	defer g.Close()
	ctx, release, e := g.Acquire(context.Background(), "batch", "p1")
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = g.Acquire(ctx, "interactive", "p1"); e != ErrNestedWorkload {
		t.Fatal(e)
	}
	canceled, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, e = g.Acquire(canceled, "interactive", "p2"); e != context.DeadlineExceeded {
		t.Fatal(e)
	}
	release()
	release()
	_, done, e := g.Acquire(context.Background(), "interactive", "p2")
	if e != nil {
		t.Fatal(e)
	}
	done()
	s := g.Snapshot()
	if s["batch"].Active != 0 || s["interactive"].Queued != 0 || s["interactive"].Completed != 1 {
		t.Fatal(s)
	}
}
func TestGovernorQueueFullAndShutdown(t *testing.T) {
	p := resourceTestDeclaration().Defaults
	p.Workloads["batch"] = WorkloadPolicy{1, 0, 1}
	g, _ := NewGovernor(p)
	_, release, _ := g.Acquire(context.Background(), "batch", "a")
	if _, _, e := g.Acquire(context.Background(), "batch", "b"); e != ErrWorkloadQueueFull {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() {
		_, done, e := g.Acquire(context.Background(), "interactive", "b")
		if done != nil {
			done()
		}
		result <- e
	}()
	g.Close()
	release()
	select {
	case e := <-result:
		if e == nil {
			t.Fatal("closed admitted")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown stuck")
	}
}
func TestResourceRouteFencingAndNoAnonymousDevAccess(t *testing.T) {
	t.Setenv("APTEVA_INSTALL_ID", "17")
	t.Setenv("APTEVA_DEPLOYMENT_ID", "deployment")
	t.Setenv("APTEVA_DEPLOYMENT_GENERATION", "3")
	t.Setenv("APTEVA_APP_TOKEN", "test-token")
	rt, e := newResourceRuntime(resourceTestDeclaration(), "")
	if e != nil {
		t.Fatal(e)
	}
	defer rt.governor.Close()
	app := &AppCtx{resources: rt}
	mux := http.NewServeMux()
	mountResourceRoutes(mux, app)
	put := func(env ResourceEnvelope, token string) int {
		b, _ := json.Marshal(env)
		req := httptest.NewRequest("PUT", "/_apteva/runtime/app/resources", strings.NewReader(string(b)))
		req.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	env := rt.envelope
	env.Revision = 1
	if code := put(env, ""); code != 401 {
		t.Fatal(code)
	}
	if code := put(env, "Bearer test-token"); code != 200 {
		t.Fatal(code)
	}
	if code := put(env, "Bearer test-token"); code != 200 {
		t.Fatal("idempotence", code)
	}
	env.Generation = "2"
	env.Revision = 2
	if code := put(env, "Bearer test-token"); code != 409 {
		t.Fatal(code)
	}
	env.Generation = "3"
	env.Revision = 1
	env.Policy.TotalConcurrency = 2
	if code := put(env, "Bearer test-token"); code != 409 {
		t.Fatal(code)
	}
	t.Setenv("APTEVA_APP_TOKEN", "")
	if code := put(env, ""); code != 401 {
		t.Fatal(code)
	}
}
func TestSocketPolicyLoopbackContinuity(t *testing.T) {
	receiver, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	defer receiver.Close()
	sender, e := net.DialUDP("udp4", nil, receiver.LocalAddr().(*net.UDPAddr))
	if e != nil {
		t.Fatal(e)
	}
	defer sender.Close()
	dscp := 46
	r := ApplyUDPSocketPolicy(receiver, SocketPolicy{ReceiveBytes: 256 << 10, SendBytes: 32 << 10, DSCP: &dscp})
	if r.DSCP == nil || *r.DSCP != 46 || r.ReceiveBytes <= 0 {
		t.Fatalf("%+v", r)
	}
	for i := 0; i < 20; i++ {
		if _, e = sender.Write([]byte{byte(i)}); e != nil {
			t.Fatal(e)
		}
	}
	_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
	for i := 0; i < 20; i++ {
		b := make([]byte, 4)
		n, _, e := receiver.ReadFromUDP(b)
		if e != nil || n != 1 || b[0] != byte(i) {
			t.Fatal(i, n, e)
		}
	}
}

func TestResourceAppCeilingsAndAllowedDSCP(t *testing.T) {
	d := resourceTestDeclaration()
	d.Ceilings = ResourceCeilings{Concurrency: 4, ReceiveBytes: 512 << 10, AllowedDSCP: []int{0, 46}}
	value := 10
	if _, e := ResolveResources(d, &ResourcePolicy{Network: map[string]SocketPolicy{"media": {DSCP: &value}}}); e == nil {
		t.Fatal("unapproved DSCP")
	}
	if _, e := ResolveResources(d, &ResourcePolicy{TotalConcurrency: 10}); e == nil {
		t.Fatal("unapproved concurrency")
	}
	value = 46
	if _, e := ResolveResources(d, &ResourcePolicy{Network: map[string]SocketPolicy{"media": {DSCP: &value, ReceiveBytes: 512 << 10}}}); e != nil {
		t.Fatal(e)
	}
}
func TestGovernorProjectFairnessAndLiveReduction(t *testing.T) {
	p := resourceTestDeclaration().Defaults
	p.Workloads["batch"] = WorkloadPolicy{Concurrency: 2, Queue: 20, Weight: 1}
	g, _ := NewGovernor(p)
	defer g.Close()
	_, release, _ := g.Acquire(context.Background(), "batch", "a")
	admitted := make(chan string, 6)
	unblock := make(chan struct{})
	start := func(project string) {
		go func() {
			_, done, e := g.Acquire(context.Background(), "batch", project)
			if e != nil {
				admitted <- "error"
				return
			}
			admitted <- project
			<-unblock
			done()
		}()
	}
	for _, project := range []string{"a", "a", "b", "c"} {
		start(project)
	}
	deadline := time.Now().Add(time.Second)
	for g.Snapshot()["batch"].Queued < 4 {
		if time.Now().After(deadline) {
			t.Fatal("queue did not fill")
		}
		time.Sleep(time.Millisecond)
	}
	p.Workloads["batch"] = WorkloadPolicy{Concurrency: 1, Queue: 20, Weight: 1}
	if e := g.Configure(p); e != nil {
		t.Fatal(e)
	}
	release()
	first := <-admitted
	if first == "a" || first == "error" {
		t.Fatalf("project rotation failed: %s", first)
	}
	close(unblock)
	for i := 0; i < 3; i++ {
		select {
		case got := <-admitted:
			if got == "error" {
				t.Fatal(got)
			}
		case <-time.After(time.Second):
			t.Fatal("starved")
		}
	}
}
