package testkit

import sdk "github.com/apteva/app-sdk"

// Option modifies the construction of test fixtures. Compose freely:
//
//	tk.NewAppCtx(t, "apteva.yaml",
//	    tk.WithProjectID("test-proj"),
//	    tk.WithEnv("PHONE_DEFAULT_COUNTRY", "GB"),
//	)
type Option func(*config)

type config struct {
	projectID string
	env       map[string]string
	cfg       map[string]string
	port      int                 // sidecar only; 0 = pick free
	emitter   *EmitRecorder       // when set, attached to the resulting AppCtx
	platform  sdk.PlatformClient  // when set, attached to the resulting AppCtx
	deps      []DependencySpec    // companion sidecars for cross-app calls
}

// DependencySpec declares a companion app the test wants spawned
// alongside the app under test. The testkit builds + starts each
// companion as its own sidecar with its own DB + token, then stands
// up a tiny gateway proxy so cross-app HTTP calls (the production
// /api/apps/<name>/* shape) work without mocks.
//
// Add with WithDependency. SourceDir is the absolute or test-relative
// path to the dependency's go.mod directory.
type DependencySpec struct {
	Name      string            // logical name, must match what the dep's manifest declares (e.g. "storage")
	SourceDir string            // path to the dep's go.mod directory; built once per process
	ProjectID string            // override APTEVA_PROJECT_ID for this dep; defaults to the parent's projectID
	Config    map[string]string // APTEVA_APP_CONFIG for the dep
	Env       map[string]string // extra env vars for the dep (rare — config + projectID cover most needs)
}

// WithProjectID sets APTEVA_PROJECT_ID for the test fixture. For
// Tier 1 it's surfaced via os.Setenv during the test (cleaned up at
// t.Cleanup). For Tier 2 it's passed to the spawned binary as an env
// var. Default: empty (treated as `scope: global` install).
func WithProjectID(id string) Option {
	return func(c *config) { c.projectID = id }
}

// WithEnv injects an environment variable. Multiple calls accumulate.
// For Tier 1, set with os.Setenv for the test's lifetime; for Tier 2,
// merged into the sidecar's env at spawn time.
func WithEnv(key, value string) Option {
	return func(c *config) {
		if c.env == nil {
			c.env = map[string]string{}
		}
		c.env[key] = value
	}
}

// WithConfig populates APTEVA_APP_CONFIG (the JSON blob the platform
// passes to a sidecar) with the given key/value pairs. For Tier 1 it
// becomes the sdk.Config attached to the AppCtx.
func WithConfig(values map[string]string) Option {
	return func(c *config) {
		if c.cfg == nil {
			c.cfg = map[string]string{}
		}
		for k, v := range values {
			c.cfg[k] = v
		}
	}
}

// WithPort pins the sidecar's listen port. Default is 0 (testkit
// picks a free port). Useful when a test wants to assert against a
// known URL.
func WithPort(port int) Option {
	return func(c *config) { c.port = port }
}

// WithPlatform attaches a stub PlatformClient to the AppCtx so tests
// can assert what was called on PlatformAPI() (ExecuteIntegrationTool,
// CallApp, etc.) and return canned responses. Without this option the
// AppCtx's platform is nil and any ctx.PlatformAPI().X() call will
// panic — fine for tests that don't touch the platform, required for
// tests of the integration-dependency wiring.
//
// Pair with a recording stub to inspect what the app called:
//
//	pf := newRecordingPlatform(map[string]any{"data":[{"url":"..."}]})
//	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(pf))
//	... call code under test ...
//	if pf.calls[0].Tool != "generate_image" { t.Fatal(...) }
func WithPlatform(p sdk.PlatformClient) Option {
	return func(c *config) { c.platform = p }
}

// WithEmitter attaches a recorder to the AppCtx so tests can assert
// what was published via ctx.Emit(). Without this option, Emit() is
// a no-op in tests (production wires an HTTP emitter; tests don't
// have a server). Pass the same recorder you'll inspect later:
//
//	rec := tk.NewEmitRecorder()
//	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec))
//	... call code that emits ...
//	if got := rec.EventsByTopic("file.added"); len(got) != 1 {
//	    t.Fatalf("expected one file.added emit, got %d", len(got))
//	}
func WithEmitter(rec *EmitRecorder) Option {
	return func(c *config) { c.emitter = rec }
}

// WithDependency declares a companion app the testkit must spawn
// alongside the app under test. Cross-app HTTP calls (the production
// /api/apps/<name>/* path that storageclient.go and friends hit via
// APTEVA_GATEWAY_URL) are routed through an in-test reverse proxy
// that swaps tokens at the boundary, so the dependency runs its real
// HTTP + auth stack — no mocks.
//
// Pass the dependency's go.mod directory as sourceDir; the testkit
// builds + caches each dep binary the same way it does the main one.
//
//	tk.SpawnSidecar(t, ".",
//	    tk.WithProjectID("test-proj"),
//	    tk.WithDependency("storage", "../storage"),
//	)
//
// Multiple deps are supported by stacking calls. v1 doesn't recurse:
// if your dep has its own deps, declare them at the top level too.
func WithDependency(name, sourceDir string, opts ...DependencyOption) Option {
	spec := DependencySpec{Name: name, SourceDir: sourceDir}
	for _, o := range opts {
		o(&spec)
	}
	return func(c *config) { c.deps = append(c.deps, spec) }
}

// DependencyOption modifies a single DependencySpec inside WithDependency.
type DependencyOption func(*DependencySpec)

// DependencyConfig sets APTEVA_APP_CONFIG on the dependency sidecar.
func DependencyConfig(cfg map[string]string) DependencyOption {
	return func(s *DependencySpec) {
		if s.Config == nil {
			s.Config = map[string]string{}
		}
		for k, v := range cfg {
			s.Config[k] = v
		}
	}
}

// DependencyEnv adds an env var to the dependency sidecar's process.
func DependencyEnv(key, value string) DependencyOption {
	return func(s *DependencySpec) {
		if s.Env == nil {
			s.Env = map[string]string{}
		}
		s.Env[key] = value
	}
}

// DependencyProjectID overrides the APTEVA_PROJECT_ID for the dep.
// Default is to reuse the parent sidecar's project id, which is
// what you want for tenant-scoped tests.
func DependencyProjectID(id string) DependencyOption {
	return func(s *DependencySpec) { s.ProjectID = id }
}

func resolveOptions(opts []Option) *config {
	c := &config{env: map[string]string{}, cfg: map[string]string{}}
	for _, o := range opts {
		o(c)
	}
	return c
}
