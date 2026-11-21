package sdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Integration simulation hand-off.
//
// A catalog provider simulation (see RuntimeIntegrationSimulationClient)
// speaks HTTP, webhooks and media WebSockets itself. Some provider traffic is
// a protocol the platform deliberately does not speak, such as SIP. For that,
// the simulation hands the work to the app that owns the simulated
// connection: it POSTs an IntegrationSimulationEvent to
// IntegrationSimulationPath on that app, and the app performs the provider
// side itself (for SIP, it places the call into its own gateway). The app
// reports what happened back into the simulation's state with
// IntegrationSimulationReporter, so tests read it through the runtime
// integration state like any other provider record.
//
// The route exists only inside an Environment (APTEVA_ENVIRONMENT_ID set),
// only for apps that implement IntegrationSimulationReceiver, and only for
// the platform's own app-token bearer. It is under /_apteva/runtime/, which
// platform gateways never forward from outside the platform.

// IntegrationSimulationPath is the app route a provider simulation hands
// work to.
const IntegrationSimulationPath = "/_apteva/runtime/integration-simulation"

// integrationSimulationBodyLimit bounds one hand-off request.
const integrationSimulationBodyLimit = 1 << 20

// IntegrationSimulationEvent is one piece of provider-side work handed to the
// app. Collection and ID name the simulation record the app reports against.
type IntegrationSimulationEvent struct {
	// Integration is the catalog slug, e.g. "telnyx".
	Integration  string `json:"integration"`
	ConnectionID int64  `json:"connection_id,omitempty"`
	// Kind is defined by the integration's simulation, e.g.
	// "sip_inbound_call".
	Kind       string          `json:"kind"`
	Collection string          `json:"collection,omitempty"`
	ID         string          `json:"id,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// IntegrationSimulationReceiver is implemented by apps that perform provider
// work a simulation hands them. The returned value, if any, is encoded as the
// JSON response and recorded with the hand-off. An error fails the hand-off
// with 422 and its message.
type IntegrationSimulationReceiver interface {
	HandleIntegrationSimulation(ctx *AppCtx, event IntegrationSimulationEvent) (any, error)
}

// IntegrationSimulationReport merges Fields into the simulation record
// Collection/ID under its "report" object. Event, when set, labels the
// report in the simulation's evidence log (e.g. "answered").
type IntegrationSimulationReport struct {
	Integration  string         `json:"integration"`
	ConnectionID int64          `json:"connection_id,omitempty"`
	Collection   string         `json:"collection"`
	ID           string         `json:"id"`
	Event        string         `json:"event,omitempty"`
	Fields       map[string]any `json:"fields"`
}

// IntegrationSimulationReporter is an optional PlatformClient extension.
// Type-assert ctx.PlatformAPI() to it; outside an Environment the platform
// refuses reports.
type IntegrationSimulationReporter interface {
	ReportIntegrationSimulation(report IntegrationSimulationReport) error
}

// IntegrationSimulationReportPath is the platform endpoint reports go to.
const IntegrationSimulationReportPath = "/api/integration-simulation/report"

func (c *httpPlatformClient) ReportIntegrationSimulation(report IntegrationSimulationReport) error {
	if err := report.validate(); err != nil {
		return err
	}
	return c.post(IntegrationSimulationReportPath, report, nil)
}

func (p *projectScopedClient) ReportIntegrationSimulation(report IntegrationSimulationReport) error {
	client, ok := p.inner.(IntegrationSimulationReporter)
	if !ok {
		return fmt.Errorf("integration simulation reports unavailable")
	}
	return client.ReportIntegrationSimulation(report)
}

var (
	_ IntegrationSimulationReporter = (*httpPlatformClient)(nil)
	_ IntegrationSimulationReporter = (*projectScopedClient)(nil)
)

func (r IntegrationSimulationReport) validate() error {
	switch {
	case strings.TrimSpace(r.Integration) == "":
		return errors.New("integration simulation report: integration is required")
	case strings.TrimSpace(r.Collection) == "" || strings.TrimSpace(r.ID) == "":
		return errors.New("integration simulation report: collection and id are required")
	case len(r.Fields) == 0:
		return errors.New("integration simulation report: fields are required")
	}
	return nil
}

// mountIntegrationSimulationRoute serves IntegrationSimulationPath for apps
// that implement IntegrationSimulationReceiver, inside an Environment only.
func mountIntegrationSimulationRoute(mux *http.ServeMux, app App, ctx *AppCtx) {
	receiver, ok := app.(IntegrationSimulationReceiver)
	if !ok || CurrentEnvironmentID() == "" {
		return
	}
	mux.HandleFunc(IntegrationSimulationPath, func(w http.ResponseWriter, r *http.Request) {
		// Never inherit a public-route exception or development no-auth mode.
		if token := os.Getenv("APTEVA_APP_TOKEN"); token == "" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, integrationSimulationBodyLimit+1))
		if err != nil || len(raw) > integrationSimulationBodyLimit {
			writeIntegrationSimulationError(w, http.StatusBadRequest, "invalid or oversized hand-off")
			return
		}
		var event IntegrationSimulationEvent
		if err := json.Unmarshal(raw, &event); err != nil || strings.TrimSpace(event.Integration) == "" || strings.TrimSpace(event.Kind) == "" {
			writeIntegrationSimulationError(w, http.StatusBadRequest, "hand-off needs integration and kind")
			return
		}
		out, err := receiver.HandleIntegrationSimulation(ctx, event)
		if err != nil {
			writeIntegrationSimulationError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if out == nil {
			out = map[string]any{"ok": true}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
}

func writeIntegrationSimulationError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
