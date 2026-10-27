// Package manifestschema derives the structural authoring schema from the Go
// manifest. ValidateManifest remains authoritative for semantic constraints.
package manifestschema

import (
	sdk "github.com/apteva/app-sdk"
	"reflect"
	"strings"
)

type object = map[string]any

func Generate() object {
	defs := object{}
	var describe func(reflect.Type) object
	describe = func(t reflect.Type) object {
		if t.Kind() == reflect.Pointer {
			return object{"anyOf": []any{describe(t.Elem()), object{"type": "null"}}}
		}
		if t.Kind() == reflect.Interface {
			return object{}
		}
		switch t.Kind() {
		case reflect.Struct:
			name := t.Name()
			if _, ok := defs[name]; !ok {
				d := object{"type": "object", "properties": object{}, "additionalProperties": false}
				defs[name] = d
				props := d["properties"].(object)
				for i := 0; i < t.NumField(); i++ {
					f := t.Field(i)
					if !f.IsExported() {
						continue
					}
					tag := strings.Split(f.Tag.Get("json"), ",")[0]
					if tag == "-" {
						continue
					}
					if tag == "" {
						tag = f.Name
					}
					props[tag] = describe(f.Type)
				}
			}
			return object{"$ref": "#/$defs/" + name}
		case reflect.Map:
			return object{"type": []string{"object", "null"}, "additionalProperties": describe(t.Elem())}
		case reflect.Slice, reflect.Array:
			return object{"type": []string{"array", "null"}, "items": describe(t.Elem())}
		case reflect.Bool:
			return object{"type": "boolean"}
		case reflect.String:
			return object{"type": "string"}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return object{"type": "integer"}
		case reflect.Float32, reflect.Float64:
			return object{"type": "number"}
		default:
			panic("unsupported manifest field: " + t.String())
		}
	}
	describe(reflect.TypeOf(sdk.Manifest{}))
	properties := func(name string) object { return defs[name].(object)["properties"].(object) }
	m := properties("Manifest")
	m["schema"] = object{"const": sdk.SchemaCurrent}
	m["name"] = object{"type": "string", "pattern": "^[a-z0-9-]+$"}
	m["version"] = object{"type": "string", "minLength": 1}
	defs["Manifest"].(object)["required"] = []string{"schema", "name", "version"}
	m["scopes"] = object{"type": []string{"array", "null"}, "items": object{"enum": []string{"project", "global"}}}
	r := properties("Runtime")
	r["kind"] = object{"enum": []string{"", "service", "source", "static"}}
	r["activation_strategy"] = object{"enum": []string{"", "auto", "exclusive"}}
	r["startup_timeout_seconds"] = object{"type": "integer", "minimum": 0, "maximum": 3600}
	r["database_upgrade"] = object{"enum": []string{"", "backward_compatible", "requires_restore"}}
	// Artifact rules are stricter than legacy static bundles.
	r["artifacts"] = object{"type": []string{"object", "null"}, "propertyNames": object{"pattern": `^[^/\\ .-]+-[^/\\ .-]+$`}, "additionalProperties": object{"type": "object", "required": []string{"url", "sha256"}, "properties": object{"url": object{"type": "string", "minLength": 1}, "sha256": object{"type": "string", "pattern": "^[a-fA-F0-9]{64}$"}}, "additionalProperties": false}}
	permissions := []string{}
	for _, p := range sdk.AllPermissions() {
		permissions = append(permissions, string(p))
	}
	properties("Requires")["permissions"] = object{"type": []string{"array", "null"}, "items": object{"enum": permissions}}
	return object{"$schema": "https://json-schema.org/draft/2020-12/schema", "$id": "https://apteva.com/schemas/apteva-app-v1.schema.json", "title": "Apteva app manifest v1", "description": "Generated structural schema. Declared object fields match the SDK; free-form maps remain open. Run apteva-app-contract validate for authoritative semantic validation and defaults.", "$ref": "#/$defs/Manifest", "$defs": defs}
}
