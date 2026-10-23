package sdk

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// WidgetSettingsUI controls presentation of per-instance settings only.
// Titles, descriptions, defaults and types remain in SettingsSchema.
type WidgetSettingsUI struct {
	Order    []string                       `yaml:"order,omitempty" json:"order,omitempty"`
	Steps    []WidgetSettingsStep           `yaml:"steps,omitempty" json:"steps,omitempty"`
	Advanced []string                       `yaml:"advanced,omitempty" json:"advanced,omitempty"`
	Fields   map[string]WidgetSettingsField `yaml:"fields,omitempty" json:"fields,omitempty"`
	// Preview opts into read-only rendering of unsaved widgetSettings with
	// widgetContext.configurationPreview=true; it never means sample data.
	Preview bool `yaml:"preview,omitempty" json:"preview,omitempty"`
}
type WidgetSettingsStep struct {
	ID     string   `yaml:"id" json:"id"`
	Title  string   `yaml:"title" json:"title"`
	Fields []string `yaml:"fields" json:"fields"`
}
type WidgetSettingsField struct {
	Discovery *WidgetSettingsDiscovery `yaml:"discovery,omitempty" json:"discovery,omitempty"`
}
type WidgetSettingsDiscovery struct {
	ConfigFieldDiscovery `yaml:",inline"`
	// Query values are JSON scalars or exactly {setting: <schema field>}.
	// References preserve the setting's scalar type, with no interpolation.
	Query map[string]any `yaml:"query,omitempty" json:"query,omitempty"`
}

func normalizeWidgetObject(value any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal(raw, &result)
	return result, err
}
func widgetQueryReference(value any) (string, bool) {
	m, ok := value.(map[string]any)
	if !ok || len(m) != 1 {
		return "", false
	}
	name, ok := m["setting"].(string)
	return name, ok && name != ""
}
func validateWidgetSettingsUI(schema map[string]any, ui *WidgetSettingsUI) error {
	if ui == nil {
		return nil
	}
	normalized, err := normalizeWidgetObject(schema)
	if err != nil {
		return err
	}
	props, _ := normalized["properties"].(map[string]any)
	if len(props) == 0 {
		return fmt.Errorf("settings_ui requires settings_schema.properties")
	}
	if len(props) > 128 || len(ui.Steps) > 16 {
		return fmt.Errorf("too many widget settings fields or steps")
	}
	refs := func(names []string) error {
		seen := map[string]bool{}
		for _, name := range names {
			if _, ok := props[name]; !ok || seen[name] {
				return fmt.Errorf("unknown or duplicate settings field %q", name)
			}
			seen[name] = true
		}
		return nil
	}
	if err := refs(ui.Order); err != nil {
		return err
	}
	if err := refs(ui.Advanced); err != nil {
		return err
	}
	ids, placed := map[string]bool{}, map[string]bool{}
	for _, step := range ui.Steps {
		if !setupID.MatchString(step.ID) || ids[step.ID] || strings.TrimSpace(step.Title) == "" || len(step.Fields) == 0 {
			return fmt.Errorf("invalid or duplicate settings step %q", step.ID)
		}
		ids[step.ID] = true
		if err := refs(step.Fields); err != nil {
			return err
		}
		for _, field := range step.Fields {
			if placed[field] || containsWidgetField(ui.Advanced, field) {
				return fmt.Errorf("settings field %q has multiple groups", field)
			}
			placed[field] = true
		}
	}
	dependencies := map[string][]string{}
	for field, metadata := range ui.Fields {
		if _, ok := props[field]; !ok {
			return fmt.Errorf("unknown settings field %q", field)
		}
		d := metadata.Discovery
		if d == nil {
			continue
		}
		if d.Tool != "" || !ValidSetupPath(d.Route) {
			return fmt.Errorf("settings field %q discovery requires an app-relative GET route", field)
		}
		property, _ := props[field].(map[string]any)
		switch property["type"] {
		case "string", "number", "integer", "boolean":
		default:
			return fmt.Errorf("discovery field %q must have a scalar type", field)
		}
		query, err := normalizeWidgetObject(d.Query)
		if err != nil {
			return err
		}
		for key, value := range query {
			switch strings.ToLower(key) {
			case "project_id", "install_id", "api_key", "file_auth", "file_agent", "mcp_token":
				return fmt.Errorf("discovery query cannot override %q", key)
			}
			if !setupID.MatchString(key) {
				return fmt.Errorf("invalid discovery query name %q", key)
			}
			if name, ok := widgetQueryReference(value); ok {
				prop, exists := props[name]
				if !exists {
					return fmt.Errorf("unknown dependency %q", name)
				}
				p, _ := prop.(map[string]any)
				switch p["type"] {
				case "string", "number", "integer", "boolean":
				default:
					return fmt.Errorf("dependency %q must have a scalar type", name)
				}
				dependencies[field] = append(dependencies[field], name)
			} else {
				switch value.(type) {
				case nil, string, float64, bool:
				default:
					return fmt.Errorf("invalid discovery query for %q", field)
				}
			}
		}
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(field string) error {
		if state[field] == 1 {
			return fmt.Errorf("cyclic widget settings dependency at %q", field)
		}
		if state[field] == 2 {
			return nil
		}
		state[field] = 1
		for _, dep := range dependencies[field] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[field] = 2
		return nil
	}
	for field := range dependencies {
		if err := visit(field); err != nil {
			return err
		}
	}
	return nil
}
func containsWidgetField(fields []string, name string) bool {
	for _, field := range fields {
		if field == name {
			return true
		}
	}
	return false
}

// ValidateWidgetSettings validates the supported primitive schema subset:
// types, enum, numeric bounds, required and additionalProperties.
func ValidateWidgetSettings(settings map[string]any, schema map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	// Manifest YAML and captured JSON can use different Go numeric types.
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	var normalizedSettings, normalizedSchema map[string]any
	if err := json.Unmarshal(settingsJSON, &normalizedSettings); err != nil {
		return err
	}
	if err := json.Unmarshal(schemaJSON, &normalizedSchema); err != nil {
		return err
	}
	settings, schema = normalizedSettings, normalizedSchema
	props, _ := schema["properties"].(map[string]any)
	for key, value := range settings {
		property, ok := props[key].(map[string]any)
		if !ok {
			if schema["additionalProperties"] == false {
				return fmt.Errorf("unknown setting %s", key)
			}
			continue
		}
		kind, _ := property["type"].(string)
		valid := true
		switch kind {
		case "string":
			_, valid = value.(string)
		case "boolean":
			_, valid = value.(bool)
		case "number", "integer":
			number, ok := value.(float64)
			valid = ok
			if valid && kind == "integer" {
				valid = number == float64(int64(number))
			}
			if min, ok := property["minimum"].(float64); ok && number < min {
				valid = false
			}
			if max, ok := property["maximum"].(float64); ok && number > max {
				valid = false
			}
		case "object":
			_, valid = value.(map[string]any)
		case "array":
			_, valid = value.([]any)
		}
		if !valid {
			return fmt.Errorf("invalid value for %s", key)
		}
		if choices, ok := property["enum"].([]any); ok {
			found := false
			for _, choice := range choices {
				if reflect.DeepEqual(choice, value) {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("invalid choice for %s", key)
			}
		}
	}
	if required, ok := schema["required"].([]any); ok {
		for _, raw := range required {
			key, _ := raw.(string)
			if _, ok := settings[key]; !ok {
				return fmt.Errorf("missing setting %s", key)
			}
		}
	}
	return nil
}
