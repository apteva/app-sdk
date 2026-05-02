package testkit

// Multi-app spawn + gateway proxy. Lets a Tier 2 test exercise the
// real cross-app HTTP path between an app and its declared
// dependencies (e.g. media → storage) without mocks.
//
// Architecture
//
//	   t            ┌──────────────┐         ┌──────────────────┐
//	────┼──────────►│ main sidecar │ ──HTTP──┤ in-test gateway  │
//	    │  spawn    │  (e.g. media)│         │  /api/apps/<x>/* │
//	    │           └──────────────┘         └──────────────────┘
//	    │                                          │   │
//	    │                                          │   └──► storage sidecar
//	    │                                          └──────► other dep sidecar
//	    │
//	    │  (each sidecar spawned separately, same SpawnSidecar machinery)
//
// The gateway is a single httptest.Server. For each declared
// dependency it owns a httputil.ReverseProxy that:
//   1. Strips the /api/apps/<name> prefix off the incoming path.
//   2. Replaces the Authorization header with the dependency's
//      own APTEVA_APP_TOKEN before forwarding (the production
//      platform proxy does the same swap).
//
// Cleanup is registered on the parent test so satellites + gateway
// die together with the main sidecar.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

// spawnDependencies spins up a sidecar per declared dependency and
// fronts them with a gateway proxy. Returns the gateway URL so the
// caller can pass it as APTEVA_GATEWAY_URL to the main sidecar.
//
// On any failure (build, spawn, healthcheck) every already-running
// satellite is killed before the test fails, so the test process
// doesn't leak children.
func spawnDependencies(t *testing.T, parentProjectID string, deps []DependencySpec) string {
	t.Helper()
	if len(deps) == 0 {
		return ""
	}

	type live struct {
		spec    DependencySpec
		sidecar *Sidecar
	}
	running := make([]*live, 0, len(deps))
	cleanupOnFail := func() {
		for _, l := range running {
			l.sidecar.Stop()
		}
	}

	for _, d := range deps {
		if d.SourceDir == "" {
			cleanupOnFail()
			t.Fatalf("testkit: dependency %q missing SourceDir", d.Name)
		}
		pid := d.ProjectID
		if pid == "" {
			pid = parentProjectID
		}
		opts := []Option{WithProjectID(pid)}
		if d.Config != nil {
			opts = append(opts, WithConfig(d.Config))
		}
		for k, v := range d.Env {
			opts = append(opts, WithEnv(k, v))
		}
		// SpawnSidecar handles build cache + healthcheck; we just
		// pass through. Any failure already calls t.Fatalf, which
		// will trigger t.Cleanup on already-registered satellites.
		sc := SpawnSidecar(t, d.SourceDir, opts...)
		running = append(running, &live{spec: d, sidecar: sc})
	}

	// Build the gateway. One reverse proxy per dep, mounted under
	// /api/apps/<name>/. The mux handles missing prefixes with a
	// clear 404 so test failures point at the right thing.
	mux := http.NewServeMux()
	for _, l := range running {
		l := l // capture
		prefix := "/api/apps/" + l.spec.Name + "/"
		mux.Handle(prefix, newDepProxy(t, l.sidecar, prefix))
		// Trailing-slash-less variant: /api/apps/storage with no
		// path segment after — proxy still wants to forward to
		// the dep's "/" so listings without a path work.
		mux.Handle("/api/apps/"+l.spec.Name, newDepProxy(t, l.sidecar, prefix))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "testkit gateway: no dep registered for "+r.URL.Path, http.StatusNotFound)
	})

	gw := httptest.NewServer(mux)
	t.Cleanup(gw.Close)
	return gw.URL
}

// newDepProxy returns a reverse proxy that forwards to a dependency
// sidecar, stripping the /api/apps/<name> prefix and swapping the
// Authorization bearer to the dep's own token (the production
// platform's apps-proxy auth does the same).
func newDepProxy(t *testing.T, sc *Sidecar, prefix string) http.Handler {
	t.Helper()
	target, err := url.Parse(sc.URL())
	if err != nil {
		t.Fatalf("testkit: parse dep URL %q: %v", sc.URL(), err)
	}
	rp := httputil.NewSingleHostReverseProxy(target)

	// httputil's default Director sets URL.Scheme/Host/Path from the
	// target + the inbound path. We layer on prefix-stripping + token
	// swap. Keep the original Director's effects by invoking it.
	defaultDir := rp.Director
	rp.Director = func(req *http.Request) {
		// Strip /api/apps/<name> so the dep sees its own root paths.
		// Use HasPrefix because the mux registered both the slash
		// and slashless variants.
		stripped := strings.TrimPrefix(req.URL.Path, strings.TrimSuffix(prefix, "/"))
		if stripped == "" {
			stripped = "/"
		}
		req.URL.Path = stripped
		req.Host = target.Host
		defaultDir(req)
		// After Director: replace bearer with the dep's token. The
		// production gateway calls this "outbound token swap". Tests
		// that don't set Authorization on the inbound request still
		// work — we just install one regardless.
		req.Header.Set("Authorization", "Bearer "+sc.Token())
	}

	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// Surface proxy errors loudly — they usually mean the dep
		// crashed mid-test.
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "testkit gateway: forward to %s failed: %v", target, err)
	}
	return rp
}
