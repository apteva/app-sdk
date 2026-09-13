package sdk

import (
	"strings"
	"testing"
)

func TestManifestArtifacts(t *testing.T) {
	valid := `schema: apteva-app/v1
name: prebuilt-test
version: 1.0.0
runtime:
  kind: service
  port: 8080
  artifacts:
    linux-arm64:
      url: https://example.com/prebuilt.tar.gz
      sha256: "` + strings.Repeat("a", 64) + `"
`
	m, err := ParseManifest([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Runtime.Artifacts) != 1 {
		t.Fatal("artifact metadata lost")
	}
	for _, bad := range []string{strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("z", 64), 1), strings.Replace(valid, "linux-arm64:", "../linux-arm64:", 1), strings.Replace(valid, "kind: service", "kind: static", 1), strings.Replace(valid, "https://example.com/prebuilt.tar.gz", `""`, 1)} {
		if _, err := ParseManifest([]byte(bad)); err == nil {
			t.Fatalf("invalid artifact accepted: %s", bad)
		}
	}
}
