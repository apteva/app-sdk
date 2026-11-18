package sdk

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// RuntimeErrMockUnsupportedTool is the error code a simulated integration
// returns for a tool its catalog simulation does not define.
const RuntimeErrMockUnsupportedTool = "mock_unsupported_tool"

// RuntimeIntegrationSimulationClient is an optional extension to
// RuntimeClient for bindings created with Simulate. Keeping it separate lets
// older runtime stubs continue to implement RuntimeClient.
type RuntimeIntegrationSimulationClient interface {
	// TriggerRuntimeIntegration causes a provider-side event, such as an
	// incoming call, on a simulated integration in the runtime.
	TriggerRuntimeIntegration(runtimeID, slug, trigger string, req RuntimeIntegrationTriggerRequest) (*RuntimeIntegrationTriggerResult, error)
	// GetRuntimeIntegrationState returns the simulated provider state and the
	// webhook deliveries so far.
	GetRuntimeIntegrationState(runtimeID, slug string, sel RuntimeIntegrationSelector) (*RuntimeIntegrationState, error)
}

// RuntimeIntegrationSelector picks one simulated binding of an integration.
// App and Role select a role binding. Leave both empty when the runtime has a
// single simulated binding for the integration.
type RuntimeIntegrationSelector struct {
	App  string `json:"app,omitempty"`
	Role string `json:"role,omitempty"`
}

type RuntimeIntegrationTriggerRequest struct {
	RuntimeIntegrationSelector
	Input map[string]any `json:"input,omitempty"`
	// Wait blocks until every event this trigger emitted was delivered or
	// failed, bounded by TimeoutMS (default 10s, max 60s). Events the apps
	// cause in reaction are delivered asynchronously; poll the state for them.
	Wait      bool `json:"wait,omitempty"`
	TimeoutMS int  `json:"timeout_ms,omitempty"`
}

// RuntimeIntegrationTriggerResult is the trigger's response and the events it
// emitted. A trigger the simulation rejects (for example a call to a number
// that is not provisioned) is an error carrying the provider-shaped body.
type RuntimeIntegrationTriggerResult struct {
	Result     json.RawMessage              `json:"result,omitempty"`
	Deliveries []RuntimeIntegrationDelivery `json:"deliveries"`
}

// RuntimeIntegrationDelivery is one simulated webhook. Target is the app path
// it was delivered to, without its query string.
type RuntimeIntegrationDelivery struct {
	ID          string     `json:"id"`
	Event       string     `json:"event"`
	EventID     string     `json:"event_id"`
	Lane        string     `json:"lane,omitempty"`
	App         string     `json:"app,omitempty"`
	Target      string     `json:"target,omitempty"`
	State       string     `json:"state"` // queued, delivering, delivered, failed, undeliverable
	Attempts    int        `json:"attempts"`
	Status      int        `json:"status,omitempty"`
	Error       string     `json:"error,omitempty"`
	OccurredAt  time.Time  `json:"occurred_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type RuntimeIntegrationState struct {
	Slug         string                       `json:"slug"`
	App          string                       `json:"app,omitempty"`
	Role         string                       `json:"role,omitempty"`
	ConnectionID int64                        `json:"connection_id"`
	Collections  map[string][]map[string]any  `json:"collections"`
	Deliveries   []RuntimeIntegrationDelivery `json:"deliveries"`
}

func runtimeIntegrationPath(runtimeID, slug string) string {
	return runtimePath(runtimeID) + "/integrations/" + url.PathEscape(strings.TrimSpace(slug))
}

func (c *httpPlatformClient) TriggerRuntimeIntegration(runtimeID, slug, trigger string, req RuntimeIntegrationTriggerRequest) (*RuntimeIntegrationTriggerResult, error) {
	var out RuntimeIntegrationTriggerResult
	path := runtimeIntegrationPath(runtimeID, slug) + "/triggers/" + url.PathEscape(strings.TrimSpace(trigger))
	client := c.client
	if req.Wait && c.slowClient != nil {
		client = c.slowClient
	}
	if err := c.postWith(client, path, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) GetRuntimeIntegrationState(runtimeID, slug string, sel RuntimeIntegrationSelector) (*RuntimeIntegrationState, error) {
	query := url.Values{}
	if sel.App != "" {
		query.Set("app", sel.App)
	}
	if sel.Role != "" {
		query.Set("role", sel.Role)
	}
	path := runtimeIntegrationPath(runtimeID, slug) + "/state"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var out RuntimeIntegrationState
	if err := c.get(path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (p *projectScopedClient) TriggerRuntimeIntegration(runtimeID, slug, trigger string, req RuntimeIntegrationTriggerRequest) (*RuntimeIntegrationTriggerResult, error) {
	client, ok := p.inner.(RuntimeIntegrationSimulationClient)
	if !ok {
		return nil, fmt.Errorf("runtime integration simulation API unavailable")
	}
	return client.TriggerRuntimeIntegration(runtimeID, slug, trigger, req)
}

func (p *projectScopedClient) GetRuntimeIntegrationState(runtimeID, slug string, sel RuntimeIntegrationSelector) (*RuntimeIntegrationState, error) {
	client, ok := p.inner.(RuntimeIntegrationSimulationClient)
	if !ok {
		return nil, fmt.Errorf("runtime integration simulation API unavailable")
	}
	return client.GetRuntimeIntegrationState(runtimeID, slug, sel)
}

var (
	_ RuntimeIntegrationSimulationClient = (*httpPlatformClient)(nil)
	_ RuntimeIntegrationSimulationClient = (*projectScopedClient)(nil)
)
