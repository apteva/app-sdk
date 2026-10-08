package sdk

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlatformAgentAppearanceCompatibility(t *testing.T) {
	var legacy PlatformAgent
	if err := json.Unmarshal([]byte(`{"id":7,"name":"Coder","status":"running"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Icon != "" || legacy.IconColor != "" {
		t.Fatal("legacy servers must leave appearance optional")
	}
	legacy.Icon, legacy.IconColor = "code", "accent"
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"icon":"code"`) || !strings.Contains(string(encoded), `"icon_color":"accent"`) {
		t.Fatalf("appearance missing: %s", encoded)
	}
	legacy.Icon, legacy.IconColor = "", ""
	encoded, _ = json.Marshal(legacy)
	if strings.Contains(string(encoded), `"icon"`) {
		t.Fatalf("empty appearance must be omitted: %s", encoded)
	}
}
