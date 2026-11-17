package sdk

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestHostUIManifestRoundTripAndValidation(t *testing.T) {
	for _, launcher := range []string{"", "auto", "hidden_on_compact", "hidden", "show", "hidden-on-mobile"} {
		for _, surface := range []string{"ui_panels", "ui_components", "ui_surfaces"} {
			t.Run(surface+"/"+launcher, func(t *testing.T) {
				entry := "slot: project.page\n      label: Demo\n      icon: chat\n      entry: /ui/Demo.mjs"
				switch surface {
				case "ui_components":
					entry = "name: demo\n      entry: /ui/Demo.mjs\n      slots: [dashboard.home]"
				case "ui_surfaces":
					entry = "id: demo\n      label: Demo\n      icon: chat\n      entry: /ui/demo.json\n      schema: apteva-native-surface/v1\n      slots: [mobile.project_app]"
				}
				raw := fmt.Sprintf("schema: apteva-app/v1\nname: demo\nversion: 1.0.0\nprovides:\n  %s:\n    - %s\n      host_ui:\n        chat_assistant:\n          launcher: %q\n", surface, entry, launcher)
				m, err := ParseManifest([]byte(raw))
				if launcher == "show" || launcher == "hidden-on-mobile" {
					if err == nil || !strings.Contains(err.Error(), "host_ui") {
						t.Fatalf("invalid launcher accepted: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				policy := func(m *Manifest) *HostUI {
					switch surface {
					case "ui_panels":
						return m.Provides.UIPanels[0].HostUI
					case "ui_components":
						return m.Provides.UIComponents[0].HostUI
					default:
						return m.Provides.UISurfaces[0].HostUI
					}
				}
				if policy(m).ChatAssistant.Launcher != ChatAssistantLauncher(launcher) {
					t.Fatal("policy lost parsing YAML")
				}
				for _, format := range []string{"json", "yaml"} {
					var restored Manifest
					if format == "json" {
						b, e := json.Marshal(m)
						if e != nil {
							t.Fatal(e)
						}
						err = json.Unmarshal(b, &restored)
					} else {
						b, e := yaml.Marshal(m)
						if e != nil {
							t.Fatal(e)
						}
						err = yaml.Unmarshal(b, &restored)
					}
					if err != nil || !reflect.DeepEqual(policy(m), policy(&restored)) {
						t.Fatalf("%s lost host policy: %v", format, err)
					}
				}
			})
		}
	}
}

func TestHostUIDefaultCompatibility(t *testing.T) {
	m := &Manifest{Schema: SchemaCurrent, Name: "legacy", Version: "1.0.0", Provides: Provides{UIPanels: []UIPanel{{Slot: "project.page", Entry: "/ui/Demo.mjs"}}}}
	if err := ValidateManifest(m); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "host_ui") {
		t.Fatal("legacy manifests must not acquire a policy")
	}
}
