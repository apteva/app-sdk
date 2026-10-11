package sdk

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestManifestActivationStrategy(t *testing.T) {
	for _, strategy := range []string{"", ActivationAuto, ActivationExclusive, "rolling", "Exclusive"} {
		t.Run(strategy, func(t *testing.T) {
			raw := "schema: apteva-app/v1\nname: service-test\nversion: 1.0.0\nruntime:\n  kind: source\n  source:\n    repo: github.com/apteva/apps\n  port: 8080\n"
			if strategy != "" {
				raw += "  activation_strategy: " + strategy + "\n"
			}
			m, err := ParseManifest([]byte(raw))
			valid := strategy == "" || strategy == ActivationAuto || strategy == ActivationExclusive
			if !valid {
				if err == nil || !strings.Contains(err.Error(), "activation_strategy") {
					t.Fatalf("validation error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if m.Runtime.ActivationStrategy != strategy {
				t.Fatalf("strategy=%q", m.Runtime.ActivationStrategy)
			}
			j, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			var fromJSON Manifest
			if err := json.Unmarshal(j, &fromJSON); err != nil {
				t.Fatal(err)
			}
			y, err := yaml.Marshal(&fromJSON)
			if err != nil {
				t.Fatal(err)
			}
			fromYAML, err := ParseManifest(y)
			if err != nil {
				t.Fatal(err)
			}
			if fromYAML.Runtime.ActivationStrategy != strategy {
				t.Fatal("activation policy lost across JSON/YAML roundtrip")
			}
		})
	}
}

func TestStaticRuntimeRejectsExclusiveActivation(t *testing.T) {
	if err := ValidateActivationStrategy(Runtime{Kind: "static", ActivationStrategy: ActivationExclusive}); err == nil {
		t.Fatal("static app accepted process activation policy")
	}
}
