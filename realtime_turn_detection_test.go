package sdk

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRealtimeSpawnRequestTurnDetectionJSON(t *testing.T) {
	request := RealtimeSpawnRequest{
		AgentID:   7,
		ThreadID:  "voice",
		Directive: "Answer callers.",
		TurnDetection: &RealtimeTurnDetection{
			Profile:           "telephony",
			StartSensitivity:  "low",
			PrefixPaddingMS:   300,
			EndSensitivity:    "low",
			SilenceDurationMS: 750,
			Interruption:      "interrupt",
		},
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"turn_detection"`,
		`"profile":"telephony"`,
		`"start_sensitivity":"low"`,
		`"prefix_padding_ms":300`,
		`"end_sensitivity":"low"`,
		`"silence_duration_ms":750`,
		`"interruption":"interrupt"`,
	} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("spawn JSON %s missing %s", data, expected)
		}
	}

	var decoded RealtimeSpawnRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.TurnDetection == nil || decoded.TurnDetection.Profile != "telephony" ||
		decoded.TurnDetection.SilenceDurationMS != 750 {
		t.Fatalf("decoded turn detection = %#v", decoded.TurnDetection)
	}
}

func TestRealtimeSpawnRequestOmitsTurnDetectionByDefault(t *testing.T) {
	data, err := json.Marshal(RealtimeSpawnRequest{
		AgentID: 7, ThreadID: "voice", Directive: "Answer callers.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "turn_detection") {
		t.Fatalf("default spawn JSON unexpectedly contains turn_detection: %s", data)
	}

	runtimeData, err := json.Marshal(RuntimeRealtimeSpawnRequest{
		ThreadID: "voice", Directive: "Answer callers.",
		TurnDetection: &RealtimeTurnDetection{Profile: "telephony"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runtimeData), `"turn_detection":{"profile":"telephony"}`) {
		t.Fatalf("runtime spawn JSON missing turn detection: %s", runtimeData)
	}
}
