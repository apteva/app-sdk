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
	// Sidecars default to loopback. Predictable APTEVA_APP_TOKENs
	// (dev-<install_id>) mean exposing every install to the LAN
	// would be a token-guessing playground — the platform proxies
	// /api/apps/<name>/* through its own auth layer instead. Apps
	// that genuinely need LAN reachability (DLNA broadcaster, IoT
	// gateways) opt in via runtime.bind_host in the manifest, or
	// override per-process with APTEVA_BIND_HOST. When non-loopback,
	// every route MUST set NoAuth (the LAN itself is the auth
	// boundary in that mode).
	host := manifest.Runtime.BindHost
	if v := os.Getenv("APTEVA_BIND_HOST"); v != "" {
		host = v
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if host != "127.0.0.1" && host != "localhost" {
		logger.Warn("sidecar binding non-loopback — every route on this app is reachable from the LAN; mark NoAuth carefully",
			"host", host, "port", port)
	}
	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", host, port),
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

// publicRoutePaths collects the patterns of every NoAuth route that
// HTTPRoutes() declared. withTokenAuth consults this slice on every
// request to decide whether to skip the bearer-token check. Populated
// once per process at mountAppRoutes time; reads after that are
// lock-free.
var publicRoutePaths []string

func mountAppRoutes(mux *http.ServeMux, app App, ctx *AppCtx) {
	for _, r := range app.HTTPRoutes() {
		if r.NoAuth {
			publicRoutePaths = append(publicRoutePaths, r.Pattern)
		}
		method := r.Method
		pattern := r.Pattern
		handler := r.Handler
		// Use Go 1.22+'s method-prefixed pattern syntax when the route
		// declares a method. Without this, two routes on the same path
		// with different methods (e.g. GET + DELETE on "/instances/",
		// a very common pattern) panic at boot because the underlying
		// ServeMux treats them as conflicting registrations.
		muxPattern := pattern
		if method != "" {
			muxPattern = method + " " + pattern
		}
		mux.HandleFunc(muxPattern, func(w http.ResponseWriter, req *http.Request) {
			handler(w, req)
			_ = ctx
		})
	}
}

// matchesPublicRoute returns true when the request path satisfies any
// NoAuth route pattern. Mirrors http.ServeMux's matching rules: an
// exact pattern matches only its exact path; a pattern ending in "/"
// is a subtree match.
func matchesPublicRoute(path string) bool {
	for _, p := range publicRoutePaths {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(path, p) {
				return true
			}
		} else if path == p {
			return true
		}
	}
	return false
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
	//
	// The handler's AppCtx has CurrentProject() pinned to the event's
	// project_id, so global apps subscribing to a multi-project topic
	// stream see each event in the right project context without
	// hand-threading.
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
		evCtx := ctx
		if ev.ProjectID != "" {
			evCtx = ctx.WithProject(ev.ProjectID)
		}
		for _, h := range app.EventHandlers() {
			if h.Topic != ev.Topic && h.Topic != "*" {
				continue
			}
			if err := h.Handler(evCtx, ev); err != nil {
				evCtx.Logger().Warn("event handler error", "topic", ev.Topic, "err", err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// runWorker drives one Worker entry — periodic if Schedule is set,
// one-shot otherwise. Cancellation honours ctx.
//
// Project dispatch. Workers run once per project per tick:
//
//   - Project-scoped install (APTEVA_PROJECT_ID set): a single-project
//     list — the worker behaves identically to pre-dispatch SDK.
//   - Global install (APTEVA_PROJECT_ID empty): the SDK calls
//     ListProjects() once per tick and runs the worker body once per
//     returned project, each invocation getting an AppCtx whose
//     CurrentProject() returns that project's id.
//
// Apps that read os.Getenv("APTEVA_PROJECT_ID") still see "" under
// global — env can't be set per-goroutine — but ctx.CurrentProject()
// returns the dispatched project. Migrating an app to global-safe
// is therefore a search-and-replace from env to CurrentProject.
func runWorker(ctx context.Context, w Worker, app *AppCtx, logger Logger) {
	dispatch := func(ctx context.Context) {
		projects := dispatchProjects(app, logger)
		for _, pid := range projects {
			scoped := app
			if pid != "" {
				scoped = app.WithProject(pid)
			}
			if err := w.Run(ctx, scoped); err != nil && ctx.Err() == nil {
				logger.Warn("worker tick error", "name", w.Name, "project", pid, "err", err)
			}
		}
	}

	if w.Schedule == "" {
		// One-shot. Run once per project.
		dispatch(ctx)
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
			dispatch(ctx)
		}
	}
}

// dispatchProjects decides which projects this tick should run for.
//
//   - APTEVA_PROJECT_ID set → singleton, that project.
//   - empty (global install) → ListProjects() from the platform.
//     A failed call yields a single empty-project tick so a
//     transient platform outage doesn't entirely silence the
//     worker; the worker body sees CurrentProject()=="" and can
//     choose to skip or proceed.
func dispatchProjects(app *AppCtx, logger Logger) []string {
	if app == nil {
		return []string{""}
	}
	if pid := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); pid != "" {
		return []string{pid}
	}
	if app.platform == nil {
		return []string{""}
	}
	projects, err := app.platform.ListProjects()
	if err != nil {
		logger.Warn("ListProjects failed; running once with empty project", "err", err)
		return []string{""}
	}
	if len(projects) == 0 {
		// No projects yet (fresh install, operator hasn't created
		// any). One empty-project tick keeps the worker alive
		// without doing per-project work.
		return []string{""}
	}
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		if p.ID == "" {
			continue
		}
		ids = append(ids, p.ID)
	}
	if len(ids) == 0 {
		return []string{""}
	}
	return ids
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
		// Per-route NoAuth carve-out. Apps mark UPnP/DLNA wire
		// endpoints (or anything else where the LAN itself is the
		// auth boundary) by setting Route.NoAuth=true; mountAppRoutes
		// collected those paths into publicRoutePaths.
		if matchesPublicRoute(r.URL.Path) {
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

		// Build a Caller from the X-Apteva-Caller-Agent header
		// (legacy: X-Apteva-Caller-Instance).
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
		// Pin the AppCtx's CurrentProject to whatever the caller
		// supplied as _project_id, so handlers reading
		// app.CurrentProject() see the right project even on a global
		// install. resolveProjectFromArgs in app code is still the
		// final word — this is for the SDK's own threading.
		handlerCtx := h.ctx
		if pid, ok := args["_project_id"].(string); ok && pid != "" {
			handlerCtx = h.ctx.WithProject(pid)
		}
		var (
			res any
			err error
		)
		switch {
		case matched.HandlerCtx != nil:
			res, err = matched.HandlerCtx(callCtx, handlerCtx, args)
		case matched.Handler != nil:
			res, err = matched.Handler(handlerCtx, args)
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
// when neither X-Apteva-Caller-Agent nor X-Apteva-Caller-Instance is
// present — the SDK treats nil as "no caller info, allow everything"
// so apps that haven't opted in keep working through platforms that
// do forward the header.
//
// X-Apteva-Caller-Agent is the canonical post-rename header name;
// the legacy X-Apteva-Caller-Instance still works during the
// deprecation window. Whichever the upstream forwards wins.
func (h *mcpHandler) buildCaller(r *http.Request) *Caller {
	raw := r.Header.Get("X-Apteva-Caller-Agent")
	if raw == "" {
		raw = r.Header.Get("X-Apteva-Caller-Instance")
	}
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
		AgentID:       id,
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

// watchDBInode logs a critical warning whenever the DB file's inode
// changes — i.e. somebody replaced the file under us. The connection
// pool drains itself via SetConnMaxLifetime; this watchdog exists so
// the swap event is loud + observable instead of showing up as
// silent SQLITE_READONLY_DBMOVED errors on the next write.
//
// Runs forever once the DB is open. Cheap (one stat every 30s) and
// the goroutine's lifetime is bounded by the process. No-ops when
// the path can't be stat'd (file gone temporarily during an atomic
// rename); the next tick will catch the replacement.
func watchDBInode(path string, logger Logger) {
	original, ok := statInode(path)
	if !ok {
		return
	}
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for range tick.C {
		current, ok := statInode(path)
		if !ok {
			continue
		}
		if current != original {
			if logger != nil {
				logger.Warn("DB file inode changed underneath running sidecar — connection pool will recycle within SetConnMaxLifetime; check for backup restore / deploy script / volume remount that replaced the file",
					"path", path, "original_inode", original, "current_inode", current)
			}
			// Update so we only log once per swap (next swap re-fires).
			original = current
		}
	}
}

func statInode(path string) (uint64, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(sys.Ino), true
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
	// Safety pragmas must use modernc.org/sqlite's `_pragma=` DSN syntax —
	// the mattn/go-sqlite3 forms `_journal_mode=…` and `_foreign_keys=…` are
	// silently ignored by modernc, which is how `ON DELETE CASCADE` and WAL
	// were no-ops in prod for months before this was caught. `_pragma=` runs
	// the listed pragma on every new pool connection, which matters because
	// `foreign_keys` and `busy_timeout` are per-connection state in SQLite.
	db, err := sql.Open("sqlite", "file:"+path+
		"?_pragma=journal_mode(WAL)"+
		"&_pragma=busy_timeout(5000)"+
		"&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	// Verify the pragmas actually took. The DSN claims them, but if the
	// driver is swapped, the syntax drifts, or someone refactors the open
	// path, we want a loud failure here rather than another months-long
	// silent no-op. Each PRAGMA query may land on a different pool
	// connection — that's fine, all conns are configured the same way.
	if err := assertSQLitePragmas(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("openAppDB %s: %w", path, err)
	}
	// Recycle pooled connections so they can't pin a stale inode forever.
	//
	// Background: when something (a backup-app live restore, an rsync, a
	// volume remount, a manual cp) replaces the DB file under the running
	// sidecar, every existing connection becomes poisoned and SQLite
	// returns SQLITE_READONLY_DBMOVED (extended code 1032) on every write.
	// The connection pool happily hands out the poisoned conns forever
	// because nothing about them looks broken to database/sql — Ping
	// passes, only writes fail.
	//
	// Capping the lifetime at 5 minutes means a poisoned pool fully drains
	// within at most 5 minutes of the swap; new connections are opened by
	// modernc.org/sqlite via a fresh open(2), which resolves the path to
	// the new inode. Idle cap of 2 minutes makes sure even quiet apps
	// recycle in a reasonable window.
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)

	// Inode watchdog. Every 30s, stat the DB path and compare the inode
	// to the one captured at open time. When the inode changes the pool
	// is poisoned (see SQLITE_READONLY_DBMOVED above); the conn-lifetime
	// cap will drain it within ~5 min, but we want loud + immediate
	// signal so operators can correlate the swap. We log a critical
	// warning rather than crashing so in-flight requests can complete;
	// the pool recycles itself within the cap window.
	go watchDBInode(path, logger)
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

// assertSQLitePragmas reads back the safety pragmas openAppDB tries to
// set and returns an error if any didn't take. Catches the class of
// bugs where DSN syntax silently mismatches the driver — historically
// modernc.org/sqlite ignored every mattn-style `_journal_mode=…`,
// `_busy_timeout=…`, `_foreign_keys=…` query param in our DSN, so FK
// enforcement and WAL were both off in prod for months.
func assertSQLitePragmas(db *sql.DB) error {
	var journal string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		return fmt.Errorf("PRAGMA journal_mode: %w", err)
	}
	if strings.ToLower(journal) != "wal" {
		return fmt.Errorf("journal_mode=%q, want wal", journal)
	}
	var busy int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		return fmt.Errorf("PRAGMA busy_timeout: %w", err)
	}
	if busy < 1000 {
		return fmt.Errorf("busy_timeout=%d, want >=1000ms", busy)
	}
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		return fmt.Errorf("PRAGMA foreign_keys: %w", err)
	}
	if fk != 1 {
		return fmt.Errorf("foreign_keys=%d, want 1 — ON DELETE CASCADE will silently no-op", fk)
	}
	return nil
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
