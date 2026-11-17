package sdk

import (
	"encoding/json"
	"testing"
)

func TestRealtimeMediaRequestWireContract(t *testing.T) {
	media := &RealtimeMediaConfig{Transport: "webrtc", Format: "g711_ulaw"}
	for _, v := range []any{RealtimeSpawnRequest{RealtimeMedia: media}, RuntimeRealtimeSpawnRequest{RealtimeMedia: media}} {
		raw, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		config, ok := got["realtime_media"].(map[string]any)
		if !ok || config["transport"] != "webrtc" || config["format"] != "g711_ulaw" {
			t.Fatalf("media selection lost: %s", raw)
		}
	}
	raw, _ := json.Marshal(RealtimeSpawnRequest{})
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if _, ok := got["realtime_media"]; ok {
		t.Fatal("default should not override media")
	}
}
