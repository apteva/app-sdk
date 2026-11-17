package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestExecuteIntegrationToolContextForwardsDeadlineAndProject(t *testing.T) {
	deadline := time.Now().Add(2 * time.Minute).Truncate(time.Millisecond)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/integrations/31/execute" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get(HeaderAppCallDeadline); got != strconv.FormatInt(deadline.UnixMilli(), 10) {
			t.Errorf("deadline header = %q", got)
		}
		var body struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Tool != "run" || body.Input["_project_id"] != "project-a" {
			t.Errorf("body = %+v", body)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"ok":true},"status":200}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	client := wrapPlatformWithProject(newHTTPPlatformClient(server.URL, "token"), "project-a")
	result, err := ExecuteIntegrationToolContext(ctx, client, 31, "run", map[string]any{"message": "hi", "_project_id": "project-a"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestExecuteIntegrationToolContextCancelsResponseRead(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":`))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := ExecuteIntegrationToolContext(ctx, newHTTPPlatformClient(server.URL, "token"), 31, "run", nil)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("response read did not cancel")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not observe cancellation")
	}
}
