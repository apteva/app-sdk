package sdk

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"reflect"
	"testing"
)

func TestRuntimePackageManifestRoundTrip(t *testing.T) {
	raw := []byte("schema: apteva-app/v1\nname: example\nversion: 1.0.0\nruntime:\n  kind: source\n  port: 8080\n  source: {repo: github.com/example/app}\n  package:\n    files: [native]\n    host_profiles: [chrome-headless]\n    app_capabilities: {passive_start: true, drain: true, routing: key}\n    build:\n      - {args: [bun, run, 'build:ui']}\n")
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	var j Manifest
	if err = json.Unmarshal(b, &j); err != nil {
		t.Fatal(err)
	}
	y, _ := yaml.Marshal(j)
	next, err := ParseManifest(y)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Runtime.Package, next.Runtime.Package) {
		t.Fatal("release metadata lost")
	}
	if got := next.Runtime.Package.HostProfiles; len(got) != 1 || got[0] != HostProfileChromeHeadless {
		t.Fatalf("host profiles = %v", got)
	}
	if err = ValidateRuntimePackage(nil); err != nil {
		t.Fatal(err)
	}
}
func TestRuntimePackageRejectsUnsafeDeclarations(t *testing.T) {
	cases := []RuntimePackage{{Files: []string{"../data"}}, {Files: []string{".secret"}}, {Files: []string{"/etc"}}, {ABI: "unknown"}, {SystemLibraries: []string{"../lib.so"}}, {Build: []RuntimeBuildCommand{{Args: []string{"sh", "-c", "echo hi"}}}}, {Build: []RuntimeBuildCommand{{Dir: "../", Args: []string{"go", "build"}}}}, {AppCapabilities: &RuntimeAppCapabilities{Routing: "guess"}}, {HostProfiles: []string{"Chrome"}}, {HostProfiles: []string{"chrome-headless", "chrome-headless"}}, {HostProfiles: []string{"-x"}}, {HostProfiles: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}}}
	for _, p := range cases {
		if ValidateRuntimePackage(&p) == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
}
