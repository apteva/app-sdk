package sdk

import (
	"strings"
	"testing"
)

func TestManifestIconStyle(t *testing.T) {
	const base = `schema: apteva-app/v1
name: icon-test
display_name: Icon Test
version: 1.0.0
icon: /ui/icon.svg
scopes: [project]
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    entry: mcp/icon-test
  port: 8080
`

	t.Run("monochrome accepted", func(t *testing.T) {
		manifest, err := ParseManifest([]byte(strings.Replace(base, "icon: /ui/icon.svg", "icon: /ui/icon.svg\nicon_style: monochrome", 1)))
		if err != nil {
			t.Fatalf("ParseManifest: %v", err)
		}
		if manifest.IconStyle != "monochrome" {
			t.Fatalf("IconStyle = %q, want monochrome", manifest.IconStyle)
		}
	})

	t.Run("legacy empty accepted", func(t *testing.T) {
		manifest, err := ParseManifest([]byte(base))
		if err != nil {
			t.Fatalf("ParseManifest: %v", err)
		}
		if manifest.IconStyle != "" {
			t.Fatalf("IconStyle = %q, want empty", manifest.IconStyle)
		}
	})

	t.Run("unknown rejected", func(t *testing.T) {
		_, err := ParseManifest([]byte(strings.Replace(base, "icon: /ui/icon.svg", "icon: /ui/icon.svg\nicon_style: animated", 1)))
		if err == nil || !strings.Contains(err.Error(), "icon_style") {
			t.Fatalf("error = %v, want icon_style validation error", err)
		}
	})

	t.Run("style requires icon", func(t *testing.T) {
		withoutIcon := strings.Replace(base, "icon: /ui/icon.svg", "icon_style: monochrome", 1)
		_, err := ParseManifest([]byte(withoutIcon))
		if err == nil || !strings.Contains(err.Error(), "icon required") {
			t.Fatalf("error = %v, want missing icon validation error", err)
		}
	})
}
