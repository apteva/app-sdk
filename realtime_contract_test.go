package sdk

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRealtimeCapabilityModeMakesEmptyExplicit(t *testing.T) {
	data, err := json.Marshal(RealtimeSpawnRequest{
		AgentID:        7,
		ThreadID:       "voice",
		Directive:      "Answer callers.",
		CapabilityMode: RealtimeCapabilitiesExplicit,
		Tools:          []string{},
		MCP:            []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"capability_mode":"explicit"`) {
		t.Fatalf("spawn JSON does not preserve explicit mode: %s", data)
	}
	if err := validateRealtimeCapabilityMode(RealtimeCapabilitiesExplicit, nil, nil); err != nil {
		t.Fatalf("explicit empty capabilities rejected: %v", err)
	}
}

func TestRealtimeCapabilityModeValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  RealtimeCapabilityMode
		tools []string
		mcp   []string
		ok    bool
	}{
		{name: "legacy", ok: true},
		{name: "inherit", mode: RealtimeCapabilitiesInheritAgent, ok: true},
		{name: "explicit", mode: RealtimeCapabilitiesExplicit, tools: []string{"bookings_check"}, ok: true},
		{name: "none", mode: RealtimeCapabilitiesNone, ok: true},
		{name: "inherit with values", mode: RealtimeCapabilitiesInheritAgent, mcp: []string{"bookings"}},
		{name: "none with values", mode: RealtimeCapabilitiesNone, tools: []string{"bookings_check"}},
		{name: "unknown", mode: "automatic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRealtimeCapabilityMode(tc.mode, tc.tools, tc.mcp)
			if (err == nil) != tc.ok {
				t.Fatalf("validation error=%v want_ok=%t", err, tc.ok)
			}
		})
	}
}

func TestRealtimeTypedCallAndTerminalContractsRoundTrip(t *testing.T) {
	request := RealtimeSpawnRequest{
		AgentID:        7,
		ThreadID:       "voice",
		Directive:      "Answer callers.",
		CapabilityMode: RealtimeCapabilitiesNone,
		CallContext: &RealtimeCallContext{
			CallID: "call-1", Direction: "inbound", Provider: "twilio",
			ProviderCallID: "CA123", FromNumber: "+12025550100", ToNumber: "+12025550101",
		},
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RealtimeSpawnRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.CallContext == nil || decoded.CallContext.CallID != "call-1" ||
		decoded.CallContext.ProviderCallID != "CA123" {
		t.Fatalf("decoded call context=%#v", decoded.CallContext)
	}

	terminal := RealtimeAudioTerminalMessage{
		Type: RealtimeAudioTerminalMessageType, Reason: RealtimeTerminalCallerDone,
		CloseCode: 1000, Detail: "caller hung up",
	}
	data, err = json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	var decodedTerminal RealtimeAudioTerminalMessage
	if err := json.Unmarshal(data, &decodedTerminal); err != nil {
		t.Fatal(err)
	}
	if decodedTerminal.Type != RealtimeAudioTerminalMessageType ||
		decodedTerminal.Reason != RealtimeTerminalCallerDone ||
		decodedTerminal.CloseCode != 1000 {
		t.Fatalf("decoded terminal=%#v", decodedTerminal)
	}
}

func TestRealtimeSpawnResultDistinguishesVerifiedNone(t *testing.T) {
	result := RealtimeSpawnResult{
		Status: "created", ThreadID: "voice",
		EffectiveTools:       []string{},
		EffectiveMCP:         []string{},
		CapabilitiesVerified: true,
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"effective_tools":[]`,
		`"effective_mcp":[]`,
		`"capabilities_verified":true`,
	} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("result JSON %s missing %s", data, expected)
		}
	}
}
