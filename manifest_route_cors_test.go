package sdk

import (
	"strings"
	"testing"
)

func routeCORSManifest(routes string) []byte {
	return []byte(`
schema: apteva-app/v1
name: previews
display_name: Previews
version: 1.0.0
provides:
  http_routes:
` + routes + `
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
  port: 8080
`)
}

func TestRouteCORSAppParses(t *testing.T) {
	m, err := ParseManifest(routeCORSManifest(`    - { prefix: /p/, no_auth: true, cors: app }
    - { prefix: /public/, no_auth: true }
    - { prefix: /private/ }`))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	routes := m.Provides.HTTPRoutes
	if len(routes) != 3 || !routes[0].AppOwnsCORS() || routes[1].AppOwnsCORS() || routes[2].AppOwnsCORS() {
		t.Fatalf("routes = %+v, want only /p/ to own its CORS", routes)
	}
}

func TestRouteCORSAppRequiresNoAuth(t *testing.T) {
	_, err := ParseManifest(routeCORSManifest(`    - { prefix: /p/, cors: app }`))
	if err == nil || !strings.Contains(err.Error(), "cors: app requires no_auth: true") {
		t.Fatalf("err = %v, want the no_auth requirement", err)
	}
	// The method helper cannot be tricked by a hand-built spec either.
	if (RouteSpec{Prefix: "/p/", CORS: RouteCORSApp}).AppOwnsCORS() {
		t.Fatal("a route without no_auth owns its CORS")
	}
}

func TestRouteCORSRejectsUnknownValue(t *testing.T) {
	for _, value := range []string{"platform", "*", "App"} {
		_, err := ParseManifest(routeCORSManifest(`    - { prefix: /p/, no_auth: true, cors: "` + value + `" }`))
		if err == nil || !strings.Contains(err.Error(), "cors must be") {
			t.Fatalf("cors %q: err = %v, want a rejection", value, err)
		}
	}
}
