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
func withTokenAuth(h http.Handler) http.Handler {
	expected := os.Getenv("APTEVA_APP_TOKEN")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /health is open so the orchestrator's probes don't need the token.
		if r.URL.Path == "/health" {
			h.ServeHTTP(w, r)
			return
		}
		if expected == "" {
			// Dev mode — no token configured. Still serve, but warn.
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
		for _, t := range h.tools {
			if t.Name == name {
				res, err := t.Handler(h.ctx, args)
				if err != nil {
					writeMCPErr(w, req.ID, -32000, err.Error())
					return
				}
				writeMCP(w, req.ID, map[string]any{"content": []map[string]any{
					{"type": "text", "text": fmt.Sprint(res)},
				}})
				return
			}
		}
		writeMCPErr(w, req.ID, -32601, "tool not found: "+name)

	default:
		writeMCPErr(w, req.ID, -32601, "method not found: "+req.Method)
	}
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
	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+cfg.Path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	if cfg.Migrations != "" {
		if err := runMigrations(db, cfg.Migrations, logger); err != nil {
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
