package sdk

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Run is the one-line entrypoint for app sidecars: read the manifest,
// open the app DB, run migrations, mount HTTP + MCP, start workers,
// block until SIGTERM. Apps' main() is typically just:
//
//	package main
//
//	import sdk "github.com/apteva/app-sdk"
//
//	func main() { sdk.Run(&MyApp{}) }
//
// The framework reads APTEVA_APP_TOKEN, APTEVA_GATEWAY_URL,
// APTEVA_INSTALL_ID, APTEVA_PROJECT_ID, APTEVA_APP_CONFIG (encoded
// JSON) from env — the platform injects all of these.
func Run(app App) {
	manifest := app.Manifest()
	if err := ValidateManifest(&manifest); err != nil {
		log.Fatalf("apteva-app: invalid manifest: %v", err)
	}

	logger := newDefaultLogger(manifest.Name)
	logger.Info("starting", "name", manifest.Name, "version", manifest.Version)

	// Open the app DB and run migrations if a db block is declared.
	var db *sql.DB
	if manifest.DB != nil {
		var err error
		db, err = openAppDB(manifest.DB, logger)
		if err != nil {
			log.Fatalf("apteva-app: open db: %v", err)
		}
		defer db.Close()
	}

	// Decode the platform-injected install config.
	cfg := readConfigEnv()

	// Platform client — speaks to apteva-server using APTEVA_APP_TOKEN.
	platform := newHTTPPlatformClient(
		os.Getenv("APTEVA_GATEWAY_URL"),
		os.Getenv("APTEVA_APP_TOKEN"),
	)

	cancelCh := make(chan struct{})
	ctx := &AppCtx{
		manifest: &manifest,
		cfg:      cfg,
		db:       db,
		platform: platform,
		logger:   logger,
		cancel:   cancelCh,
		emitter:  newHTTPEmitter(os.Getenv("APTEVA_GATEWAY_URL"), os.Getenv("APTEVA_APP_TOKEN"), logger),
	}

	if err := app.OnMount(ctx); err != nil {
		log.Fatalf("apteva-app: OnMount: %v", err)
	}

	// HTTP mux: app routes + framework routes (/health, /mcp, /events).
	mux := http.NewServeMux()
	mountAppRoutes(mux, app, ctx)
	mountFrameworkRoutes(mux, app, ctx)

	port := manifest.Runtime.Port
	if port == 0 {
		port = 8080 // dev default
	}
	// APTEVA_APP_PORT — platform-injected when multiple apps run on one
	// host so they don't collide on the manifest's static port. Local
	// installer picks a free port per install.
	if v := os.Getenv("APTEVA_APP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			port = n
		}
	}
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           withTokenAuth(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Start workers — each in its own goroutine, supervised.
	var wg sync.WaitGroup
	workerCtx, workerCancel := context.WithCancel(context.Background())
	for _, w := range app.Workers() {
		wg.Add(1)
		go func(w Worker) {
			defer wg.Done()
			runWorker(workerCtx, w, ctx, logger)
		}(w)
	}

	// Boot HTTP server in its own goroutine so we can listen for signals.
	go func() {
		logger.Info("listening", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("apteva-app: http: %v", err)
		}
	}()

	// Wait for SIGTERM/SIGINT.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
	logger.Info("shutting down")

	close(cancelCh)
	workerCancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	if err := app.OnUnmount(ctx); err != nil {
		logger.Warn("OnUnmount error", "err", err)
	}
	wg.Wait()
	logger.Info("stopped")
}

func mountAppRoutes(mux *http.ServeMux, app App, ctx *AppCtx) {
	for _, r := range app.HTTPRoutes() {
		method := r.Method
		pattern := r.Pattern
		handler := r.Handler
		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			if method != "" && req.Method != method {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			handler(w, req)
			_ = ctx
		})
	}
}

// mountFrameworkRoutes wires the platform-mandated endpoints every app
// must expose: /health (orchestrator), /mcp (agent calls), /events
// (platform pushes events). If a ./ui directory exists, /ui/* serves
// it as static files so the dashboard can iframe panel HTML directly.
func mountFrameworkRoutes(mux *http.ServeMux, app App, ctx *AppCtx) {
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	// Live manifest — apteva-server polls this so the dashboard's
	// "update available" detector can see the running sidecar's version
	// rather than the snapshot frozen in apps.manifest_json at install
	// time. Same shape as the parsed manifest stored server-side.
	mux.HandleFunc("/manifest", func(w http.ResponseWriter, _ *http.Request) {
		m := app.Manifest()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&m)
	})

	// Static UI bundle. The default lookup is ./ui; apps that ship UI
	// override by setting APTEVA_UI_DIR.
	uiDir := os.Getenv("APTEVA_UI_DIR")
	if uiDir == "" {
		uiDir = "ui"
	}
	if info, err := os.Stat(uiDir); err == nil && info.IsDir() {
		mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.Dir(uiDir))))
	}

	// MCP endpoint — JSON-RPC over HTTP. Minimal implementation: list
	// tools / call tool. Apps may override by declaring no tools and
	// mounting their own /mcp route in HTTPRoutes(), but most won't
	// need to.
	mcp := newMCPHandler(app, ctx)
	mux.Handle("/mcp", mcp)

	// Event ingestion — the platform POSTs platform events here and
	// the framework dispatches to the app's EventHandlers.
	mux.HandleFunc("/events", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var ev Event
		if err := json.NewDecoder(req.Body).Decode(&ev); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		for _, h := range app.EventHandlers() {
			if h.Topic != ev.Topic && h.Topic != "*" {
				continue
			}
			if err := h.Handler(ctx, ev); err != nil {
				ctx.Logger().Warn("event handler error", "topic", ev.Topic, "err", err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// runWorker drives one Worker entry — periodic if Schedule is set,
// one-shot otherwise. Cancellation honours ctx.
func runWorker(ctx context.Context, w Worker, app *AppCtx, logger Logger) {
	if w.Schedule == "" {
		// One-shot. Run once.
		if err := w.Run(ctx, app); err != nil && ctx.Err() == nil {
			logger.Warn("worker exited", "name", w.Name, "err", err)
		}
		return
	}
	interval, err := parseSchedule(w.Schedule)
	if err != nil {
		logger.Warn("worker schedule parse failed; not running", "name", w.Name, "schedule", w.Schedule, "err", err)
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.Run(ctx, app); err != nil && ctx.Err() == nil {
				logger.Warn("worker tick error", "name", w.Name, "err", err)
			}
		}
	}
}

// parseSchedule supports the "@every <duration>" syntax for now. Real
// cron expressions can be added later via robfig/cron.
func parseSchedule(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	const prefix = "@every "
	if strings.HasPrefix(s, prefix) {
		return time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(s, prefix)))
	}
	return 0, fmt.Errorf("only '@every <duration>' schedules supported; got %q", s)
}

// withTokenAuth gates every non-/health request on a header that
// matches APTEVA_APP_TOKEN. The platform injects the token in env at
// boot and uses it on every callback. Without this, anyone reaching
// the sidecar's port could call its tools.
//
// Three carve-outs let apps serve signed / public URLs / provider
// webhooks:
//
//  1. /health — the orchestrator's liveness probe never has a token.
//  2. ?sig=… — any GET request with a sig query param falls through
//     to the app handler, which is responsible for verifying the
//     HMAC. This is the standard pattern for time-limited signed
//     URLs (S3 presign, CDN presign, etc.) and lets anonymous chat
//     users / external links work without leaking the platform
//     token. The handler MUST verify the sig itself.
//  3. /webhooks/* — provider-callback endpoints (SES/SNS, Twilio,
//     Stripe, GitHub, etc.). The provider signs the request payload
//     with their own scheme (SNS X.509, Twilio HMAC-SHA1, Stripe
//     signed payload, etc.); the handler MUST verify that signature.
//     This carve-out exists because external providers don't have
//     our APTEVA_APP_TOKEN and can't be made to use one — their
//     authenticity comes from per-provider request signing.
func withTokenAuth(h http.Handler) http.Handler {
	expected := os.Getenv("APTEVA_APP_TOKEN")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			h.ServeHTTP(w, r)
			return
		}
		if expected == "" {
			// Dev mode — no token configured. Still serve, but warn.
			h.ServeHTTP(w, r)
			return
		}
		// Signed-URL pass-through. Only GETs (no mutations); the app
		// handler verifies the actual sig.
		if r.Method == http.MethodGet && r.URL.Query().Get("sig") != "" {
			h.ServeHTTP(w, r)
			return
		}
		// Webhook pass-through. Handler is responsible for verifying
		// the provider's signature on the payload — see comment block
		// above.
		if strings.HasPrefix(r.URL.Path, "/webhooks/") {
			h.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("Authorization")
		if got != "Bearer "+expected {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// readConfigEnv decodes APTEVA_APP_CONFIG (JSON object of string → string)
// the platform injects at boot. Empty or missing → empty config.
func readConfigEnv() Config {
	raw := os.Getenv("APTEVA_APP_CONFIG")
	if raw == "" {
		return Config{}
	}
	var c Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		log.Printf("apteva-app: APTEVA_APP_CONFIG malformed (%v) — using empty", err)
		return Config{}
	}
	return c
}

// --- minimal MCP handler ----------------------------------------------------

type mcpHandler struct {
	tools []Tool
	app   App
	ctx   *AppCtx
}

func newMCPHandler(app App, ctx *AppCtx) http.Handler {
	tools := app.MCPTools()
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return &mcpHandler{tools: tools, app: app, ctx: ctx}
}

type mcpRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

type mcpResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *mcpError `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (h *mcpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req mcpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeMCPErr(w, nil, -32700, "parse error")
		return
	}
	switch req.Method {
	case "initialize":
		// MCP handshake — clients (apteva-core's MCP transport
		// included) send this first and refuse to load tools when
		// the server returns -32601. Reply with the standard
		// initialize response so tools/list works on the next call.
		writeMCP(w, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo": map[string]any{
				"name":    h.ctx.Manifest().Name,
				"version": h.ctx.Manifest().Version,
			},
		})

	case "notifications/initialized":
		// Notification, no response expected. Some clients send it
		// after `initialize`; just ack with an empty success.
		writeMCP(w, req.ID, map[string]any{})

	case "tools/list":
		out := make([]map[string]any, 0, len(h.tools))
		for _, t := range h.tools {
			out = append(out, map[string]any{
				"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema,
			})
		}
		writeMCP(w, req.ID, map[string]any{"tools": out})

	case "tools/call":
		name, _ := req.Params["name"].(string)
		args, _ := req.Params["arguments"].(map[string]any)
		var matched *Tool
		for i := range h.tools {
			if h.tools[i].Name == name {
				matched = &h.tools[i]
				break
			}
		}
		if matched == nil {
			writeMCPErr(w, req.ID, -32601, "tool not found: "+name)
			return
		}

		// Build a Caller from the X-Apteva-Caller-Instance header.
		// When the header is absent (back-compat: older platforms
		// that don't forward it, dev curl, etc.) caller is nil and
		// the gate degrades to "allow" — same as today.
		caller := h.buildCaller(r)

		// Manifest-declared per-tool gate. Only kicks in when both
		// Requires is set on this tool's MCPToolSpec AND a Caller
		// was supplied. Apps that don't declare requires keep
		// pre-permissions behavior.
		if caller != nil {
			spec := h.toolSpec(name)
			if spec != nil && spec.Requires != "" {
				resource, err := substituteResource(spec.ResourceFrom, args)
				if err != nil {
					writeMCPErr(w, req.ID, -32602, err.Error())
					return
				}
				if !caller.Allows(spec.Requires, resource) {
					writeMCPErr(w, req.ID, -32000,
						(&ErrForbidden{Permission: spec.Requires, Resource: resource}).Error())
					return
				}
			}
		}

		callCtx := WithCaller(r.Context(), caller)
		var (
			res any
			err error
		)
		switch {
		case matched.HandlerCtx != nil:
			res, err = matched.HandlerCtx(callCtx, h.ctx, args)
		case matched.Handler != nil:
			res, err = matched.Handler(h.ctx, args)
		default:
			writeMCPErr(w, req.ID, -32603, "tool "+name+": no handler registered")
			return
		}
		if err != nil {
			writeMCPErr(w, req.ID, -32000, err.Error())
			return
		}
		// Encode as JSON so MCP clients can parse the result
		// back into structured data. fmt.Sprint produces Go's
		// map syntax which no client understands.
		body, jerr := json.Marshal(res)
		if jerr != nil {
			writeMCPErr(w, req.ID, -32000, "encode result: "+jerr.Error())
			return
		}
		writeMCP(w, req.ID, map[string]any{"content": []map[string]any{
			{"type": "text", "text": string(body)},
		}})

	default:
		writeMCPErr(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

// buildCaller materializes a Caller from request headers. Returns nil
// when X-Apteva-Caller-Instance is absent — the SDK treats nil as
// "no caller info, allow everything" so apps that haven't opted in
// keep working through platforms that do forward the header.
func (h *mcpHandler) buildCaller(r *http.Request) *Caller {
	raw := r.Header.Get("X-Apteva-Caller-Instance")
	if raw == "" {
		return nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	resp, _ := h.ctx.platform.GetGrants(id)
	if resp == nil {
		resp = &GrantsResponse{DefaultEffect: "allow"}
	}
	return &Caller{
		InstanceID:    id,
		Grants:        resp.Grants,
		DefaultEffect: resp.DefaultEffect,
		Resources:     h.ctx.Manifest().Provides.Resources,
	}
}

// toolSpec finds the manifest spec for a runtime tool by name.
// Returns nil when the manifest doesn't list it (e.g. dynamic tools
// not declared up front) — those tools skip the gate.
func (h *mcpHandler) toolSpec(name string) *MCPToolSpec {
	specs := h.ctx.Manifest().Provides.MCPTools
	for i := range specs {
		if specs[i].Name == name {
			return &specs[i]
		}
	}
	return nil
}

func writeMCP(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeMCPErr(w http.ResponseWriter, id any, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: msg}})
}

// --- DB open + migrations ---------------------------------------------------

func openAppDB(cfg *DBConfig, logger Logger) (*sql.DB, error) {
	if cfg.Driver != "sqlite" && cfg.Driver != "" {
		return nil, fmt.Errorf("only sqlite supported in this SDK; got %q", cfg.Driver)
	}
	// DB_PATH env wins over the manifest's path. The platform sets
	// it per-install to avoid two installs of the same app writing
	// to the manifest's hard-coded path. The testkit also relies on
	// this so spawned sidecars in tests don't share /data/<app>.db.
	path := cfg.Path
	if v := os.Getenv("DB_PATH"); v != "" {
		path = v
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	migrationsDir := cfg.Migrations
	// APTEVA_MIGRATIONS_DIR overrides the manifest path the same way
	// DB_PATH overrides cfg.Path. The platform points it at the absolute
	// migrations dir inside the cloned source tree so apps don't have to
	// know where their source landed on disk — relative manifest paths
	// like "migrations/" only work when CWD happens to be the source
	// dir, which it isn't for spawned sidecars.
	if v := os.Getenv("APTEVA_MIGRATIONS_DIR"); v != "" {
		migrationsDir = v
	}
	if migrationsDir != "" {
		if err := runMigrations(db, migrationsDir, logger); err != nil {
			return nil, err
		}
	}
	return db, nil
}

func runMigrations(db *sql.DB, dir string, logger Logger) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS _migrations (
		filename TEXT PRIMARY KEY,
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create _migrations: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		var seen string
		err := db.QueryRow("SELECT filename FROM _migrations WHERE filename = ?", f).Scan(&seen)
		if err == nil {
			continue // already applied
		}
		body, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return err
		}
		if _, err := db.Exec(string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", f, err)
		}
		if _, err := db.Exec("INSERT INTO _migrations(filename) VALUES (?)", f); err != nil {
			return err
		}
		logger.Info("applied migration", "file", f)
	}
	return nil
}

// --- default logger ---------------------------------------------------------

type defaultLogger struct{ tag string }

func newDefaultLogger(tag string) Logger { return &defaultLogger{tag: tag} }

func (l *defaultLogger) Info(msg string, kv ...any)  { l.emit("INFO", msg, kv) }
func (l *defaultLogger) Warn(msg string, kv ...any)  { l.emit("WARN", msg, kv) }
func (l *defaultLogger) Error(msg string, kv ...any) { l.emit("ERR", msg, kv) }

func (l *defaultLogger) emit(level, msg string, kv []any) {
	parts := []string{fmt.Sprintf("[%s] [%s] %s", level, l.tag, msg)}
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, fmt.Sprintf("%v=%v", kv[i], kv[i+1]))
	}
	log.Println(strings.Join(parts, " "))
}
