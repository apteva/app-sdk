package testkit

import (
	"encoding/json"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// IntegrationTriggerCall records one TriggerRuntimeIntegration call.
type IntegrationTriggerCall struct {
	RuntimeID string
	Slug      string
	Trigger   string
	Request   sdk.RuntimeIntegrationTriggerRequest
}

// IntegrationSimulationStub implements sdk.RuntimeIntegrationSimulationClient
// for tests of apps that drive simulated integrations in a runtime. Embed it
// next to your RuntimeClient stub. Triggers are recorded; OnTrigger, when
// set, supplies the result, and States answers state reads keyed by
// "<runtimeID>/<slug>".
type IntegrationSimulationStub struct {
	mu        sync.Mutex
	triggers  []IntegrationTriggerCall
	OnTrigger func(call IntegrationTriggerCall) (*sdk.RuntimeIntegrationTriggerResult, error)
	States    map[string]*sdk.RuntimeIntegrationState
}

var _ sdk.RuntimeIntegrationSimulationClient = (*IntegrationSimulationStub)(nil)

func (s *IntegrationSimulationStub) TriggerRuntimeIntegration(runtimeID, slug, trigger string, req sdk.RuntimeIntegrationTriggerRequest) (*sdk.RuntimeIntegrationTriggerResult, error) {
	call := IntegrationTriggerCall{RuntimeID: runtimeID, Slug: slug, Trigger: trigger, Request: req}
	s.mu.Lock()
	s.triggers = append(s.triggers, call)
	handler := s.OnTrigger
	s.mu.Unlock()
	if handler != nil {
		return handler(call)
	}
	return &sdk.RuntimeIntegrationTriggerResult{Result: json.RawMessage(`{}`), Deliveries: []sdk.RuntimeIntegrationDelivery{}}, nil
}

func (s *IntegrationSimulationStub) GetRuntimeIntegrationState(runtimeID, slug string, _ sdk.RuntimeIntegrationSelector) (*sdk.RuntimeIntegrationState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, ok := s.States[runtimeID+"/"+slug]; ok {
		return state, nil
	}
	return nil, ErrNotImplemented
}

// Triggers returns the recorded trigger calls in order.
func (s *IntegrationSimulationStub) Triggers() []IntegrationTriggerCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]IntegrationTriggerCall(nil), s.triggers...)
}
