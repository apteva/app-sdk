package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAppContextCancellationDuringResponseRead(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "batch"}[batch], func(t *testing.T) {
			started, stopped := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{"partial":`))
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
				close(stopped)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := wrapPlatformWithProject(newHTTPPlatformClient(server.URL, "token"), "project")
			done := make(chan error, 1)
			go func() {
				var err error
				if batch {
					_, err = CallAppBatchContext(ctx, client, "tables", []AppCall{{Tool: "read"}}, AppBatchOptions{Execution: ParallelIndependent, Concurrency: 2})
				} else {
					_, err = CallAppContext(ctx, client, "tables", "read", nil)
				}
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
					t.Fatalf("error=%v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("read did not cancel")
			}
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("downstream not canceled")
			}
		})
	}
}

func TestDirectJSONNegotiationDoesNotConfuseApplicationErrorField(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "direct"}[direct], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(HeaderAppResultFormat) != AppResultJSON {
					t.Error("no negotiation")
				}
				if direct {
					w.Header().Set(HeaderAppResultFormat, AppResultJSON)
					_, _ = w.Write([]byte(`{"error":{"message":"application data"},"result":{"content":[]}}`))
				} else {
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"{\"error\":{\"message\":\"application data\"},\"result\":{\"content\":[]}}"}]}}`))
				}
			}))
			defer server.Close()
			var out map[string]any
			err := CallAppResultContext(context.Background(), newHTTPPlatformClient(server.URL, "token"), "tables", "get", nil, &out)
			if err != nil || out["error"] == nil {
				t.Fatalf("out=%v err=%v", out, err)
			}
		})
	}
}

func TestSourceSDKDirectJSONAndLegacyCompatibility(t *testing.T) {
	app := &metaTestApp{}
	manifest := app.Manifest()
	h := newMCPHandler(app, &AppCtx{manifest: &manifest})
	for _, mode := range []string{"legacy", "direct", "unbound", "unknown-tool"} {
		t.Run(mode, func(t *testing.T) {
			tool := "reply"
			if mode == "unknown-tool" {
				tool = "missing"
			}
			r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"`+tool+`","arguments":{}}}`))
			if mode != "legacy" {
				r.Header.Set(HeaderAppResultFormat, AppResultJSON)
			}
			if mode != "unbound" {
				r.Header.Set(HeaderBoundCallerInstallID, "123")
				r.Header.Set(HeaderBoundCallerAppName, "caller")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			var out map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if mode == "direct" {
				if w.Header().Get(HeaderAppResultFormat) != AppResultJSON || out["ok"] != true || out["jsonrpc"] != nil {
					t.Fatal(w.Body.String())
				}
			} else if w.Header().Get(HeaderAppResultFormat) != "" || out["jsonrpc"] != "2.0" {
				t.Fatal(w.Body.String())
			}
			if mode == "unknown-tool" && out["error"] == nil {
				t.Fatal("error lost")
			}
		})
	}
}

func TestContextBatchOptionsAndProjectScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Calls []AppCall
			AppBatchOptions
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Execution != ParallelIndependent || body.Concurrency != 3 || body.ResultMode != "json" || body.Calls[0].Input["_project_id"] != "project-a" {
			t.Errorf("request=%+v", body)
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	input := map[string]any{"limit": 10}
	client := wrapPlatformWithProject(newHTTPPlatformClient(server.URL, "token"), "project-a")
	_, err := CallAppBatchContext(context.Background(), client, "tables", []AppCall{{Tool: "read", Input: input}}, AppBatchOptions{Execution: ParallelIndependent, Concurrency: 3, ResultMode: "json"})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := input["_project_id"]; exists {
		t.Fatal("mutated caller input")
	}
}

type deadlineTestApp struct {
	metaTestApp
	deadline time.Time
}

func (a *deadlineTestApp) MCPTools() []Tool {
	return []Tool{{Name: "reply", HandlerCtx: func(ctx context.Context, _ *AppCtx, _ map[string]any) (any, error) {
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(a.deadline) {
			return nil, errors.New("deadline not propagated")
		}
		return map[string]any{"ok": true}, nil
	}}}
}

func TestSDKDeadlinePropagation(t *testing.T) {
	deadline := time.Now().Add(time.Minute).Truncate(time.Millisecond)
	app := &deadlineTestApp{deadline: deadline}
	manifest := app.Manifest()
	h := newMCPHandler(app, &AppCtx{manifest: &manifest})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(HeaderAppCallDeadline) != strconv.FormatInt(deadline.UnixMilli(), 10) {
			t.Error("deadline header absent")
		}
		r = httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"reply","arguments":{}}}`)).WithContext(r.Context())
		r.Header.Set(HeaderAppCallDeadline, strconv.FormatInt(deadline.UnixMilli(), 10))
		r.Header.Set(HeaderBoundCallerInstallID, "1")
		r.Header.Set(HeaderAppResultFormat, AppResultJSON)
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	var out map[string]any
	if err := CallAppResultContext(ctx, newHTTPPlatformClient(server.URL, "token"), "tables", "get", nil, &out); err != nil || out["ok"] != true {
		t.Fatalf("out=%v err=%v", out, err)
	}
}

func TestBatchResultDecode(t *testing.T) {
	for _, result := range []AppCallResult{
		{Format: "json", Result: json.RawMessage(`{"ok":true}`)},
		{Result: json.RawMessage(`{"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"{\"ok\":true}"}]}}`)},
	} {
		var out map[string]any
		if err := result.Decode(&out); err != nil || out["ok"] != true {
			t.Fatalf("out=%v err=%v", out, err)
		}
	}
	if err := (AppCallResult{Error: &AppCallError{Message: "failure"}}).Decode(&struct{}{}); err == nil {
		t.Fatal("child error lost")
	}
}

type benchmarkResultApp struct {
	metaTestApp
	result any
}

func (a *benchmarkResultApp) MCPTools() []Tool {
	return []Tool{{Name: "reply", Handler: func(_ *AppCtx, _ map[string]any) (any, error) { return a.result, nil }}}
}

func BenchmarkAppResultEncoding(b *testing.B) {
	app := &benchmarkResultApp{result: map[string]any{"rows": strings.Repeat("payload with quotes \" and escaped data ", 1000)}}
	manifest := app.Manifest()
	h := newMCPHandler(app, &AppCtx{manifest: &manifest})
	for _, mode := range []string{"mcp", "json"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"reply","arguments":{}}}`))
				r.Header.Set(HeaderBoundCallerInstallID, "1")
				if mode == "json" {
					r.Header.Set(HeaderAppResultFormat, AppResultJSON)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 200 {
					b.Fatal(w.Body.String())
				}
			}
		})
	}
}
