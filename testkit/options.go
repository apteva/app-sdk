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

func resolveOptions(opts []Option) *config {
	c := &config{env: map[string]string{}, cfg: map[string]string{}}
	for _, o := range opts {
		o(c)
	}
	return c
}
