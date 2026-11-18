package testkit

import (
	"encoding/json"
	"errors"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestIntegrationSimulationStubRecordsTriggers(t *testing.T) {
	stub := &IntegrationSimulationStub{
		OnTrigger: func(call IntegrationTriggerCall) (*sdk.RuntimeIntegrationTriggerResult, error) {
			if call.Trigger != "incoming_call" {
				return nil, errors.New("unexpected trigger")
			}
			return &sdk.RuntimeIntegrationTriggerResult{Result: json.RawMessage(`{"call_control_id":"v3:1"}`)}, nil
		},
		States: map[string]*sdk.RuntimeIntegrationState{"rt-1/telnyx": {Slug: "telnyx", ConnectionID: 4}},
	}
	var client sdk.RuntimeIntegrationSimulationClient = stub
	result, err := client.TriggerRuntimeIntegration("rt-1", "telnyx", "incoming_call", sdk.RuntimeIntegrationTriggerRequest{Input: map[string]any{"to": "+15550100001"}})
	if err != nil || string(result.Result) != `{"call_control_id":"v3:1"}` {
		t.Fatalf("result=%v err=%v", result, err)
	}
	calls := stub.Triggers()
	if len(calls) != 1 || calls[0].RuntimeID != "rt-1" || calls[0].Request.Input["to"] != "+15550100001" {
		t.Fatalf("calls = %#v", calls)
	}
	if state, err := client.GetRuntimeIntegrationState("rt-1", "telnyx", sdk.RuntimeIntegrationSelector{}); err != nil || state.ConnectionID != 4 {
		t.Fatalf("state=%v err=%v", state, err)
	}
	if _, err := client.GetRuntimeIntegrationState("rt-2", "telnyx", sdk.RuntimeIntegrationSelector{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("unknown state err = %v", err)
	}
}
