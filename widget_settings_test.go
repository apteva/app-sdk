package sdk

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
)

const widgetSettingsManifest = `schema: apteva-app/v1
name: widget-test
version: 0.1.0
runtime:
  kind: static
  static_dir: /tmp/widget-test-ui
provides:
  ui_components:
    - name: metric
      entry: /ui/Metric.mjs
      slots: [dashboard.home]
      settings_schema:
        type: object
        required: [app, topic]
        properties:
          app: {type: string, title: App}
          topic: {type: integer, title: Event}
          window: {type: integer, minimum: 1, maximum: 30, default: 7}
      settings_ui:
        order: [app, topic, window]
        advanced: [window]
        preview: true
        steps:
          - {id: data, title: Choose data, fields: [app, topic]}
        fields:
          topic:
            discovery:
              route: /widget-options
              query:
                field: topic
                app: {setting: app}
                limit: 20
              response_path: options
              value_field: id
              label_field: label
`

func TestWidgetSettingsMetadataRoundTrip(t *testing.T) {
	var m Manifest
	if err := yaml.Unmarshal([]byte(widgetSettingsManifest), &m); err != nil {
		t.Fatal(err)
	}
	if err := validateUIComponents(m.Provides.UIComponents); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Manifest
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	c := decoded.Provides.UIComponents[0]
	if !c.SettingsUI.Preview || c.SettingsUI.Fields["topic"].Discovery.Route != "/widget-options" {
		t.Fatal("metadata lost")
	}
	if c.SettingsUI.Fields["topic"].Discovery.Query["limit"] != float64(20) {
		t.Fatal("literal type lost")
	}
	// The public parser must also recognize the optional declaration.
	if _, err = ParseManifest([]byte(widgetSettingsManifest)); err != nil {
		t.Fatal(err)
	}
}
func TestWidgetSettingsDeclarationValidation(t *testing.T) {
	for name, replace := range map[string][2]string{
		"unknown field":      {"order: [app, topic, window]", "order: [missing]"},
		"duplicate field":    {"order: [app, topic, window]", "order: [app, app]"},
		"unknown dependency": {"{setting: app}", "{setting: missing}"},
		"self cycle":         {"{setting: app}", "{setting: topic}"},
		"external route":     {"route: /widget-options", "route: https://example.invalid/options"},
		"traversal":          {"route: /widget-options", "route: /foo/../options"},
		"scope override":     {"field: topic", "project_id: other"},
		"invalid reference":  {"{setting: app}", "{setting: app, extra: true}"},
		"duplicate groups":   {"fields: [app, topic]", "fields: [app, topic, window]"},
		"duplicate steps":    {"- {id: data, title: Choose data, fields: [app, topic]}", "- {id: data, title: Choose data, fields: [app]}\n          - {id: data, title: Duplicate, fields: [topic]}"},
	} {
		t.Run(name, func(t *testing.T) {
			var m Manifest
			err := yaml.Unmarshal([]byte(strings.Replace(widgetSettingsManifest, replace[0], replace[1], 1)), &m)
			if err == nil {
				err = validateUIComponents(m.Provides.UIComponents)
			}
			if err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	// A cycle involving two valid fields must also fail.
	schema := map[string]any{"properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}}}
	ui := &WidgetSettingsUI{Fields: map[string]WidgetSettingsField{}}
	for a, b := range map[string]string{"a": "b", "b": "a"} {
		ui.Fields[a] = WidgetSettingsField{Discovery: &WidgetSettingsDiscovery{ConfigFieldDiscovery: ConfigFieldDiscovery{Route: "/options"}, Query: map[string]any{"value": map[string]any{"setting": b}}}}
	}
	if validateWidgetSettingsUI(schema, ui) == nil {
		t.Fatal("two-field cycle accepted")
	}
}
func TestWidgetSettingsValidationKeepsTypedValues(t *testing.T) {
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"count", "enabled"}, "properties": map[string]any{"count": map[string]any{"type": "integer", "minimum": 0, "maximum": 4, "enum": []int{0, 2, 4}}, "enabled": map[string]any{"type": "boolean"}}}
	if err := ValidateWidgetSettings(map[string]any{"count": 0, "enabled": false}, schema); err != nil {
		t.Fatal(err)
	}
	for _, value := range []map[string]any{{"count": "2", "enabled": true}, {"count": 1.5, "enabled": true}, {"count": 3, "enabled": true}, {"count": 6, "enabled": true}, {"enabled": true}, {"count": 2, "enabled": true, "unknown": 1}} {
		if ValidateWidgetSettings(value, schema) == nil {
			t.Fatalf("invalid settings accepted: %v", value)
		}
	}
}
