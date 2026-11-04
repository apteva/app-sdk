package sdk

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// threadProviderPlatform fakes a server. capability is what it advertises; a
// negative value means it has no platform-info route at all.
type threadProviderPlatform struct {
	capability int
	infoStatus int
	status     int
	body       string
	infoCalls  atomic.Int32
	spawnCalls atomic.Int32
	lastBody   atomic.Value
}

func (p *threadProviderPlatform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/apps/callback/platform-info":
		p.infoCalls.Add(1)
		if p.infoStatus != 0 {
			http.Error(w, "platform info failed", p.infoStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"thread_provider_selection": p.capability})
	case "/api/apps/callback/threads/spawn", "/api/apps/callback/threads/ensure":
		p.spawnCalls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.lastBody.Store(body)
		if p.status != 0 {
			http.Error(w, p.body, p.status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "created", "provider": map[string]any{"pin": "anthropic", "active": "anthropic"},
		})
	default:
		http.NotFound(w, r)
	}
}

func threadProviderClient(t *testing.T, platform *threadProviderPlatform) ThreadProfileClient {
	t.Helper()
	t.Setenv("APTEVA_PUBLIC_URL", "")
	ts := httptest.NewServer(platform)
	t.Cleanup(ts.Close)
	return newHTTPPlatformClient(ts.URL, "test-token").(ThreadProfileClient)
}

func threadProviderCalls(client ThreadProfileClient, provider string) map[string]func() error {
	return map[string]func() error{
		"spawn": func() error {
			_, err := client.(ThreadClient).SpawnThread(ThreadSpawnRequest{AgentID: 1, ThreadID: "t", Provider: provider})
			return err
		},
		"ensure": func() error {
			_, err := client.EnsureThread(ThreadEnsureRequest{ThreadSpawnRequest: ThreadSpawnRequest{AgentID: 1, ThreadID: "t", Provider: provider}})
			return err
		},
	}
}

func TestThreadProviderOmittedSkipsTheCheckAndSendsNothing(t *testing.T) {
	platform := &threadProviderPlatform{capability: 0}
	for name, call := range threadProviderCalls(threadProviderClient(t, platform), "") {
		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, sent := platform.lastBody.Load().(map[string]any)["provider"]; sent {
			t.Fatalf("%s sent a provider it wasn't given", name)
		}
	}
	if got := platform.infoCalls.Load(); got != 0 {
		t.Fatalf("checked capability %d times for a request with no provider", got)
	}
}

func TestThreadProviderWhitespaceCountsAsOmitted(t *testing.T) {
	platform := &threadProviderPlatform{capability: 0}
	for name, call := range threadProviderCalls(threadProviderClient(t, platform), "   ") {
		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, sent := platform.lastBody.Load().(map[string]any)["provider"]; sent {
			t.Fatalf("%s sent a blank provider", name)
		}
	}
}

func TestThreadProviderFailsBeforeCreatingOnAnOlderServer(t *testing.T) {
	for name, platform := range map[string]*threadProviderPlatform{
		"advertises nothing": {capability: 0},
		"no platform-info":   {infoStatus: http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			for call, run := range threadProviderCalls(threadProviderClient(t, platform), "anthropic") {
				if err := run(); !errors.Is(err, ErrThreadProviderUnsupported) {
					t.Fatalf("%s err = %v, want ErrThreadProviderUnsupported", call, err)
				}
			}
			if got := platform.spawnCalls.Load(); got != 0 {
				t.Fatalf("reached the spawn route %d times on an unsupported server", got)
			}
		})
	}
}

func TestThreadProviderFailedCheckIsNotReportedAsAnOlderServer(t *testing.T) {
	platform := &threadProviderPlatform{infoStatus: http.StatusBadGateway}
	for call, run := range threadProviderCalls(threadProviderClient(t, platform), "anthropic") {
		err := run()
		if err == nil || errors.Is(err, ErrThreadProviderUnsupported) {
			t.Fatalf("%s err = %v, want a retryable failure", call, err)
		}
	}
	if got := platform.spawnCalls.Load(); got != 0 {
		t.Fatalf("reached the spawn route %d times without a capability answer", got)
	}
}

func TestThreadProviderIsForwardedAndReported(t *testing.T) {
	platform := &threadProviderPlatform{capability: ThreadProviderSelectionVersion}
	client := threadProviderClient(t, platform)
	spawned, err := client.(ThreadClient).SpawnThread(ThreadSpawnRequest{AgentID: 1, ThreadID: "t", Provider: " anthropic "})
	if err != nil {
		t.Fatal(err)
	}
	if got := platform.lastBody.Load().(map[string]any)["provider"]; got != "anthropic" {
		t.Fatalf("spawn provider = %v", got)
	}
	if spawned.Provider == nil || spawned.Provider.Pin != "anthropic" || spawned.Provider.Active != "anthropic" {
		t.Fatalf("spawn status = %+v", spawned.Provider)
	}
	ensured, err := client.EnsureThread(ThreadEnsureRequest{ThreadSpawnRequest: ThreadSpawnRequest{AgentID: 1, ThreadID: "t", Provider: "anthropic"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := platform.lastBody.Load().(map[string]any)["provider"]; got != "anthropic" {
		t.Fatalf("ensure provider = %v", got)
	}
	if ensured.Provider == nil || ensured.Provider.Pin != "anthropic" {
		t.Fatalf("ensure status = %+v", ensured.Provider)
	}
	if got := platform.infoCalls.Load(); got != 1 {
		t.Fatalf("capability fetched %d times, want it cached", got)
	}
}

func TestThreadProviderRefusalsHaveTheirOwnErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusConflict, ErrThreadProviderConflict},
		{http.StatusUnprocessableEntity, ErrThreadProviderUnavailable},
	} {
		platform := &threadProviderPlatform{capability: ThreadProviderSelectionVersion, status: tc.status, body: "the reason\n"}
		for call, run := range threadProviderCalls(threadProviderClient(t, platform), "anthropic") {
			err := run()
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), "the reason") {
				t.Fatalf("%s status %d err = %v, want %v with the server's reason", call, tc.status, err, tc.want)
			}
		}
	}
}

func TestThreadProviderOtherFailuresAreLeftAlone(t *testing.T) {
	platform := &threadProviderPlatform{capability: ThreadProviderSelectionVersion, status: http.StatusBadGateway, body: "core down"}
	for call, run := range threadProviderCalls(threadProviderClient(t, platform), "anthropic") {
		err := run()
		if err == nil || errors.Is(err, ErrThreadProviderConflict) || errors.Is(err, ErrThreadProviderUnavailable) {
			t.Fatalf("%s err = %v", call, err)
		}
	}
}

// Requests without a provider must keep today's errors, whatever the status.
func TestThreadProviderStatusCodesDoNotChangeOrdinaryErrors(t *testing.T) {
	platform := &threadProviderPlatform{capability: ThreadProviderSelectionVersion, status: http.StatusConflict, body: "busy"}
	for call, run := range threadProviderCalls(threadProviderClient(t, platform), "") {
		err := run()
		if err == nil || errors.Is(err, ErrThreadProviderConflict) {
			t.Fatalf("%s err = %v", call, err)
		}
		if !strings.Contains(err.Error(), "http 409: busy") {
			t.Fatalf("%s changed the error format: %v", call, err)
		}
	}
}

func TestProjectScopedClientForwardsThreadProvider(t *testing.T) {
	platform := &threadProviderPlatform{capability: ThreadProviderSelectionVersion}
	t.Setenv("APTEVA_PUBLIC_URL", "")
	ts := httptest.NewServer(platform)
	defer ts.Close()
	scoped := (&projectScopedClient{inner: newHTTPPlatformClient(ts.URL, "test-token"), projectID: "project-7"})
	if _, err := scoped.SpawnThread(ThreadSpawnRequest{AgentID: 1, ThreadID: "t", Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	body := platform.lastBody.Load().(map[string]any)
	if body["provider"] != "anthropic" || body["project_id"] != "project-7" {
		t.Fatalf("body = %v", body)
	}
}
