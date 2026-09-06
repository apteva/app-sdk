package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type startupTestApp struct {
	metaTestApp
	mount  func(*AppCtx) error
	budget int
}

func (a *startupTestApp) Manifest() Manifest {
	m := a.metaTestApp.Manifest()
	m.Schema = SchemaCurrent
	m.Runtime.StartupTimeoutSeconds = a.budget
	return m
}
func (a *startupTestApp) OnMount(c *AppCtx) error { return a.mount(c) }
func TestStartupGate(t *testing.T) {
	s := newStartupStatus()
	s.progress("tables", 3, 55)
	for _, path := range []string{"/health", "/mcp", "/tables"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 503 {
			t.Fatal(path, w.Code)
		}
		if path == "/health" && !strings.Contains(w.Body.String(), `"completed":3`) {
			t.Fatal(w.Body.String())
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if s.ready(canceled, http.NotFoundHandler()) {
		t.Fatal("canceled startup became ready")
	}
	s.failIfInitializing()
	if s.ready(context.Background(), http.NotFoundHandler()) {
		t.Fatal("failed startup became ready")
	}
}
func TestRunAppInitializationAndCancellation(t *testing.T) {
	for _, mode := range []string{"ready", "cancel", "legacy_cancel", "deadline", "failure"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			t.Setenv("APTEVA_APP_PORT", fmt.Sprint(port))
			t.Setenv("APTEVA_APP_TOKEN", "")
			entered := make(chan struct{})
			release := make(chan struct{})
			app := &startupTestApp{budget: 1, mount: func(c *AppCtx) error {
				c.ReportStartupProgress("tables", 1, 55)
				close(entered)
				if mode == "legacy_cancel" {
					<-c.Done()
					return context.Canceled
				}
				select {
				case <-c.StartupContext().Done():
					return c.StartupContext().Err()
				case <-release:
				}
				if mode == "failure" {
					return errors.New("mount failed")
				}
				return nil
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- runApp(ctx, app) }()
			select {
			case <-entered:
			case err := <-done:
				t.Fatal(err)
			case <-time.After(3 * time.Second):
				t.Fatal("mount never entered")
			}
			base := fmt.Sprintf("http://127.0.0.1:%d", port)
			client := &http.Client{Timeout: time.Second}
			for _, path := range []string{"/health", "/mcp"} {
				resp, err := client.Get(base + path)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 503 {
					t.Fatalf("premature readiness %d %s", resp.StatusCode, body)
				}
			}
			switch mode {
			case "cancel", "legacy_cancel":
				cancel()
			case "deadline": // Let the SDK's absolute deadline expire.
			default:
				close(release)
			}
			if mode == "ready" {
				end := time.Now().Add(time.Second)
				ready := false
				for time.Now().Before(end) {
					resp, err := client.Get(base + "/health")
					if err == nil {
						resp.Body.Close()
						if resp.StatusCode == 200 {
							ready = true
							break
						}
					}
					time.Sleep(time.Millisecond)
				}
				if !ready {
					t.Fatal("never ready after successful mount")
				}
				cancel()
			}
			select {
			case err := <-done:
				if mode == "ready" && err != nil {
					t.Fatal(err)
				}
				if mode != "ready" && err == nil {
					t.Fatal("failed mount succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("startup cancellation did not terminate")
			}
		})
	}
}
