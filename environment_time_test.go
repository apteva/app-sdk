package sdk

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnvironmentTimeReadsLiveServerClock(t *testing.T) {
	initial := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var current atomic.Int64
	current.Store(initial.Unix())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/environment-time" {
			t.Errorf("path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"current_time":"` + time.Unix(current.Load(), 0).UTC().Format(time.RFC3339Nano) + `"}`))
	}))
	defer server.Close()
	t.Setenv("APTEVA_ENVIRONMENT_ID", "rt-test")
	t.Setenv("APTEVA_MANUAL_CLOCK", "1")
	ctx := &AppCtx{platform: newHTTPPlatformClient(server.URL, "test")}
	if got, err := ctx.EnvironmentTime(); err != nil || !got.Equal(initial) {
		t.Fatalf("EnvironmentTime=%s %v", got, err)
	}
	if got := ctx.Now(); !got.Equal(initial) {
		t.Fatalf("Now=%s", got)
	}
	current.Store(initial.Add(time.Hour).Unix())
	if got := ctx.Now(); !got.Equal(initial.Add(time.Hour)) {
		t.Fatalf("advanced Now=%s", got)
	}
}
