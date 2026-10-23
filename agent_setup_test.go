package sdk

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
)

func TestAgentSetupManifestRoundTrip(t *testing.T) {
	const base = "schema: apteva-app/v1\nname: arbitrary-app\nversion: 1.0.0\nruntime:\n  kind: static\n  static_dir: /tmp/arbitrary-ui\n"
	for _, suffix := range []string{"", "agent_setup:\n  recommended: true\n  reason: Useful tools for your agent.\n", "agent_setup:\n  recommended: false\n"} {
		m, err := ParseManifest([]byte(base + suffix))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var restored Manifest
		if err = json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		out, err := yaml.Marshal(restored)
		if err != nil {
			t.Fatal(err)
		}
		again, err := ParseManifest(out)
		if err != nil {
			t.Fatal(err)
		}
		if suffix == "" && again.AgentSetup != nil {
			t.Fatal("legacy manifest gained a recommendation")
		}
		if strings.Contains(suffix, "true") && (again.AgentSetup == nil || !again.AgentSetup.Recommended || again.AgentSetup.Reason != "Useful tools for your agent.") {
			t.Fatal("recommendation lost")
		}
	}
	for _, suffix := range []string{"agent_setup:\n  recommended: invalid\n", "agent_setup:\n  reason: " + strings.Repeat("界", 301) + "\n"} {
		if _, err := ParseManifest([]byte(base + suffix)); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
}
