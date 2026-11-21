package sdk

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type handoffApp struct {
	metaTestApp
	got  []IntegrationSimulationEvent
	fail error
}

func (a *handoffApp) HandleIntegrationSimulation(_ *AppCtx, event IntegrationSimulationEvent) (any, error) {
	a.got = append(a.got, event)
	if a.fail != nil {
		return nil, a.fail
	}
	return map[string]any{"accepted": event.ID}, nil
}

func handoffRequest(t *testing.T, mux *http.ServeMux, method, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, IntegrationSimulationPath, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestIntegrationSimulationRouteOnlyInsideEnvironment(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "app-token")
	t.Setenv("APTEVA_ENVIRONMENT_ID", "")
	mux := http.NewServeMux()
	mountIntegrationSimulationRoute(mux, &handoffApp{}, nil)
	if rec := handoffRequest(t, mux, http.MethodPost, "Bearer app-token", `{"integration":"telnyx","kind":"k"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("outside an environment: status %d, want 404", rec.Code)
	}

	t.Setenv("APTEVA_ENVIRONMENT_ID", "rt_env_1")
	mux = http.NewServeMux()
	mountIntegrationSimulationRoute(mux, &metaTestApp{}, nil)
	if rec := handoffRequest(t, mux, http.MethodPost, "Bearer app-token", `{"integration":"telnyx","kind":"k"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("app without a receiver: status %d, want 404", rec.Code)
	}
}

func TestIntegrationSimulationRouteDeliversEvents(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "app-token")
	t.Setenv("APTEVA_ENVIRONMENT_ID", "rt_env_1")
	app := &handoffApp{}
	mux := http.NewServeMux()
	mountIntegrationSimulationRoute(mux, app, nil)
	body := `{"integration":"telnyx","connection_id":7,"kind":"sip_inbound_call","collection":"sip_calls","id":"sip_1","payload":{"to":"+12025550100"}}`

	if rec := handoffRequest(t, mux, http.MethodPost, "", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: status %d", rec.Code)
	}
	if rec := handoffRequest(t, mux, http.MethodPost, "Bearer other", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer: status %d", rec.Code)
	}
	if rec := handoffRequest(t, mux, http.MethodGet, "Bearer app-token", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status %d", rec.Code)
	}
	if rec := handoffRequest(t, mux, http.MethodPost, "Bearer app-token", `{"integration":"telnyx"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing kind: status %d", rec.Code)
	}
	if len(app.got) != 0 {
		t.Fatalf("rejected requests reached the app: %+v", app.got)
	}

	rec := handoffRequest(t, mux, http.MethodPost, "Bearer app-token", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out["accepted"] != "sip_1" {
		t.Fatalf("response %s (%v)", rec.Body, err)
	}
	got := app.got[0]
	if got.Integration != "telnyx" || got.ConnectionID != 7 || got.Kind != "sip_inbound_call" || got.Collection != "sip_calls" || got.ID != "sip_1" || !strings.Contains(string(got.Payload), "+12025550100") {
		t.Fatalf("event %+v", got)
	}

	app.fail = errors.New("no SIP gateway")
	rec = handoffRequest(t, mux, http.MethodPost, "Bearer app-token", body)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "no SIP gateway") {
		t.Fatalf("handler error: %d %s", rec.Code, rec.Body)
	}
}

func TestIntegrationSimulationRouteRefusesDevelopmentNoAuth(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "")
	t.Setenv("APTEVA_ENVIRONMENT_ID", "rt_env_1")
	mux := http.NewServeMux()
	mountIntegrationSimulationRoute(mux, &handoffApp{}, nil)
	if rec := handoffRequest(t, mux, http.MethodPost, "", `{"integration":"telnyx","kind":"k"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no app token configured: status %d, want 401", rec.Code)
	}
}

func TestReportIntegrationSimulationPostsToPlatform(t *testing.T) {
	t.Setenv("APTEVA_ENVIRONMENT_ID", "rt_env_1")
	var got IntegrationSimulationReport
	var headers http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != IntegrationSimulationReportPath || r.Method != http.MethodPost {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		headers = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := &httpPlatformClient{baseURL: server.URL, token: "app-token", client: server.Client(), slowClient: server.Client()}
	var reporter IntegrationSimulationReporter = client
	report := IntegrationSimulationReport{Integration: "telnyx", ConnectionID: 7, Collection: "sip_calls", ID: "sip_1", Event: "answered", Fields: map[string]any{"state": "answered"}}
	if err := reporter.ReportIntegrationSimulation(report); err != nil {
		t.Fatal(err)
	}
	if got.Integration != "telnyx" || got.ID != "sip_1" || got.Fields["state"] != "answered" || got.Event != "answered" {
		t.Fatalf("report %+v", got)
	}
	if headers.Get("Authorization") != "Bearer app-token" || headers.Get("X-Apteva-Environment-Id") != "rt_env_1" {
		t.Fatalf("headers %v", headers)
	}
	for _, bad := range []IntegrationSimulationReport{
		{Collection: "c", ID: "i", Fields: map[string]any{"a": 1}},
		{Integration: "telnyx", ID: "i", Fields: map[string]any{"a": 1}},
		{Integration: "telnyx", Collection: "c", ID: "i"},
	} {
		if err := reporter.ReportIntegrationSimulation(bad); err == nil {
			t.Fatalf("invalid report accepted: %+v", bad)
		}
	}
}
