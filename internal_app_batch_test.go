package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type internalBatchTestApp struct {
	metaTestApp
	tools []Tool
}

func (a *internalBatchTestApp) MCPTools() []Tool          { return a.tools }
func (a *internalBatchTestApp) PreserveJSONNumbers() bool { return true }

func runInternalBatch(t *testing.T, handler http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, InternalAppBatchPath, strings.NewReader(body))
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestInternalAppBatchUsesSharedToolPathAndStructuredResults(t *testing.T) {
	app := &internalBatchTestApp{tools: []Tool{
		{Name: "echo", HandlerCtx: func(ctx context.Context, _ *AppCtx, args map[string]any) (any, error) {
			caller := CallerFrom(ctx)
			if caller == nil || caller.AppInstallID != 41 || caller.AppName != "graphql" || caller.SubjectID != "9" {
				return nil, errors.New("trusted caller context missing")
			}
			return args, nil
		}},
		{Name: "fail", Handler: func(*AppCtx, map[string]any) (any, error) { return nil, errors.New("expected failure") }},
	}}
	manifest := app.Manifest()
	handler := newInternalAppBatchHandler(newMCPHandler(app, &AppCtx{manifest: &manifest}))
	rec := runInternalBatch(t, handler, `{"calls":[{"id":"a","tool":"echo","input":{"n":9007199254740993}},{"id":"b","tool":"fail","input":{}},{"id":"c","tool":"missing","input":{}}]}`, map[string]string{
		HeaderBoundCallerInstallID: "41",
		HeaderBoundCallerAppName:   "graphql",
		"X-Apteva-Subject-Type":    "user",
		"X-Apteva-Subject-ID":      "9",
	})
	if rec.Code != http.StatusOK || rec.Header().Get(HeaderInternalAppBatchVersion) != InternalAppBatchVersion {
		t.Fatalf("status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	var response InternalAppBatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 3 || response.Results[0].ID != "a" || response.Results[0].Format != "json" {
		t.Fatalf("results=%+v", response.Results)
	}
	if !strings.Contains(string(response.Results[0].Result), `9007199254740993`) {
		t.Fatalf("number precision lost: %s", response.Results[0].Result)
	}
	if response.Results[1].Error == nil || response.Results[1].Error.Code != -32000 || response.Results[2].Error == nil || response.Results[2].Error.Code != -32601 {
		t.Fatalf("per-call errors lost: %+v", response.Results)
	}
}

func TestInternalAppBatchBoundedParallelOrderingAndCancellation(t *testing.T) {
	var active, peak atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	app := &internalBatchTestApp{tools: []Tool{{Name: "work", HandlerCtx: func(ctx context.Context, _ *AppCtx, args map[string]any) (any, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
			return args, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}}}
	manifest := app.Manifest()
	handler := newInternalAppBatchHandler(newMCPHandler(app, &AppCtx{manifest: &manifest}))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- runInternalBatch(t, handler, `{"execution":"parallel_independent","concurrency":2,"calls":[{"id":"1","tool":"work","input":{"i":1}},{"id":"2","tool":"work","input":{"i":2}},{"id":"3","tool":"work","input":{"i":3}}]}`, map[string]string{HeaderBoundCallerInstallID: "1", HeaderBoundCallerAppName: "caller"})
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("parallel calls did not start")
		}
	}
	select {
	case <-started:
		close(release)
		t.Fatal("concurrency bound exceeded")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	rec := <-done
	var response InternalAppBatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 || len(response.Results) != 3 {
		t.Fatalf("peak=%d results=%+v", peak.Load(), response.Results)
	}
	for i, id := range []string{"1", "2", "3"} {
		if response.Results[i].ID != id || response.Results[i].Error != nil {
			t.Fatalf("results reordered or failed: %+v", response.Results)
		}
	}
}

func TestInternalAppBatchRequiresTargetTokenAndBoundCaller(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "target-token")
	app := &internalBatchTestApp{tools: []Tool{{Name: "ok", Handler: func(*AppCtx, map[string]any) (any, error) { return map[string]any{"ok": true}, nil }}}}
	manifest := app.Manifest()
	mux := http.NewServeMux()
	mountFrameworkRoutes(mux, app, &AppCtx{manifest: &manifest})
	handler := withTokenAuth(mux)
	body := `{"calls":[{"tool":"ok","input":{}}]}`
	if rec := runInternalBatch(t, handler, body, map[string]string{HeaderBoundCallerInstallID: "1", HeaderBoundCallerAppName: "caller"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing target token status=%d", rec.Code)
	}
	if rec := runInternalBatch(t, handler, body, map[string]string{"Authorization": "Bearer target-token"}); rec.Code != http.StatusForbidden {
		t.Fatalf("missing bound caller status=%d", rec.Code)
	}
	if rec := runInternalBatch(t, handler, body, map[string]string{"Authorization": "Bearer target-token", HeaderBoundCallerInstallID: "1", HeaderBoundCallerAppName: "caller"}); rec.Code != http.StatusOK {
		t.Fatalf("valid internal request status=%d body=%s", rec.Code, rec.Body.String())
	}
}
