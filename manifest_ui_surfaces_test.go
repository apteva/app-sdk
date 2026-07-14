package sdk

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseManifestUISurface(t *testing.T) {
	raw := []byte(`
schema: apteva-app/v1
name: storage
display_name: Storage
version: 1.0.0
provides:
  ui_surfaces:
    - id: files
      label: Files
      icon: folder
      schema: apteva-native-surface/v1
      entry: /ui/surfaces/files.json
      slots: [mobile.project_app]
`)
	manifest, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Provides.UISurfaces) != 1 || manifest.Provides.UISurfaces[0].ID != "files" {
		t.Fatalf("unexpected surfaces: %#v", manifest.Provides.UISurfaces)
	}

	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"ui_surfaces"`) {
		t.Fatalf("JSON omitted ui_surfaces: %s", encoded)
	}
}

func TestManifestUISurfaceValidation(t *testing.T) {
	valid := UISurface{
		ID: "files", Label: "Files", Icon: "folder",
		Schema: NativeSurfaceSchemaCurrent,
		Entry:  "/ui/surfaces/files.json",
		Slots:  []string{UISurfaceSlotMobileProjectApp},
	}
	tests := []struct {
		name     string
		surfaces []UISurface
		want     string
	}{
		{"duplicate id", []UISurface{valid, valid}, "duplicate id"},
		{"missing label", []UISurface{func() UISurface { v := valid; v.Label = ""; return v }()}, "label required"},
		{"bad schema", []UISurface{func() UISurface { v := valid; v.Schema = "v2"; return v }()}, "unsupported"},
		{"external entry", []UISurface{func() UISurface { v := valid; v.Entry = "https://example.com/files.json"; return v }()}, "under /ui/"},
		{"traversal", []UISurface{func() UISurface { v := valid; v.Entry = "/ui/../secret.json"; return v }()}, "traversal-free"},
		{"encoded traversal", []UISurface{func() UISurface { v := valid; v.Entry = "/ui/%2e%2e/secret.json"; return v }()}, "traversal-free"},
		{"unknown slot", []UISurface{func() UISurface { v := valid; v.Slots = []string{"desktop"}; return v }()}, "unsupported slot"},
		{"duplicate slot", []UISurface{func() UISurface {
			v := valid
			v.Slots = []string{UISurfaceSlotMobileProjectApp, UISurfaceSlotMobileProjectApp}
			return v
		}()}, "duplicate slot"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := &Manifest{Schema: SchemaCurrent, Name: "storage", Version: "1.0.0"}
			manifest.Provides.UISurfaces = test.surfaces
			err := ValidateManifest(manifest)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestManifestWithoutUISurfacesRemainsValid(t *testing.T) {
	manifest := &Manifest{Schema: SchemaCurrent, Name: "legacy", Version: "1.0.0"}
	if err := ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
}
