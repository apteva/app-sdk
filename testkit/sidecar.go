package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Sidecar is a running app binary the test can talk to. Spawned by
// SpawnSidecar; auto-killed on t.Cleanup.
//
// Each Sidecar gets its own port and its own temp data directory.
// Build artefacts are cached per-process so spawning many sidecars
// doesn't repeatedly invoke `go build`.
type Sidecar struct {
	url     string
	cmd     *exec.Cmd
	dataDir string
	token   string
	t       *testing.T
}

// SpawnSidecar builds (if needed) and starts the binary at appDir.
// It waits for /health to return 200 before returning. Failure to
// build, start, or become healthy fails the test immediately.
//
// Layout: appDir is the directory containing the app's go.mod and
// main package. Migrations directory is resolved relative to the
// manifest the binary embeds; the binary handles that itself.
func SpawnSidecar(t *testing.T, appDir string, opts ...Option) *Sidecar {
	t.Helper()
	c := resolveOptions(opts)

	bin := buildSidecar(t, appDir)
	port := c.port
	if port == 0 {
		port = pickFreePort(t)
	}
	dataDir := t.TempDir()
	token := "test-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	env := os.Environ()
	env = append(env,
		"APTEVA_APP_PORT="+strconv.Itoa(port),
		"APTEVA_APP_TOKEN="+token,
		"APTEVA_PROJECT_ID="+c.projectID,
		"DB_PATH="+filepath.Join(dataDir, "app.db"),
	)
	if cfgJSON, err := json.Marshal(c.cfg); err == nil {
		env = append(env, "APTEVA_APP_CONFIG="+string(cfgJSON))
	}
	for k, v := range c.env {
		env = append(env, k+"="+v)
	}

	cmd := exec.Command(bin)
	cmd.Dir = appDir
	cmd.Env = env
	stdout := &lockedBuffer{}
	stderr := &lockedBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("testkit: start sidecar: %v", err)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	sc := &Sidecar{
		url:     url,
		cmd:     cmd,
		dataDir: dataDir,
		token:   token,
		t:       t,
	}

	if err := waitHealthy(url+"/health", 10*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("testkit: sidecar didn't become healthy: %v\n--- stdout ---\n%s\n--- stderr ---\n%s",
			err, stdout.String(), stderr.String())
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		// Surface logs only on test failure for cheap signal:
		if t.Failed() {
			t.Logf("sidecar stdout:\n%s", stdout.String())
			t.Logf("sidecar stderr:\n%s", stderr.String())
		}
	})
	return sc
}

// URL returns the sidecar's base URL (no trailing slash). Useful for
// raw http calls when the typed helpers below don't fit.
func (s *Sidecar) URL() string { return s.url }

// Token returns the auth bearer for this sidecar. Pre-filled into
// every helper-issued request; expose for raw clients that need it.
func (s *Sidecar) Token() string { return s.token }

// Stop terminates the sidecar early. Cleanup at t.Cleanup runs the
// same path; calling Stop yourself is optional, useful if you want to
// assert behaviour after shutdown.
func (s *Sidecar) Stop() {
	_ = s.cmd.Process.Kill()
	_ = s.cmd.Wait()
}

// ─── HTTP helpers ──────────────────────────────────────────────────

// MCP calls a tool via JSON-RPC and returns the parsed result map
// (the MCP-shape `content[]` wrapper is unwrapped). Test fails on
// transport / RPC errors.
func (s *Sidecar) MCP(tool string, args map[string]any) map[string]any {
	s.t.Helper()
	res, err := s.MCPRaw("tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		s.t.Fatalf("testkit: MCP %q: %v", tool, err)
	}
	return res
}

// MCPRaw exposes the JSON-RPC layer for tests that need to assert
// exact MCP response shapes (errors, non-tools-call methods, etc.).
func (s *Sidecar) MCPRaw(method string, params map[string]any) (map[string]any, error) {
	body := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	}
	raw, _ := json.Marshal(body)
	resp, err := s.do("POST", "/mcp", bytes.NewReader(raw), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode mcp response: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("mcp error %d: %s", out.Error.Code, out.Error.Message)
	}
	// `tools/call` wraps the actual tool return in `content[].text`.
	// Most apps return a JSON object; unwrap if so, otherwise return
	// the result map verbatim.
	if content, ok := out.Result["content"].([]any); ok && len(content) > 0 {
		if first, ok := content[0].(map[string]any); ok {
			if text, ok := first["text"].(string); ok {
				// First, try to JSON-decode the text — most app tool
				// handlers return structured maps.
				var inner map[string]any
				if err := json.Unmarshal([]byte(text), &inner); err == nil {
					return inner, nil
				}
				// Fallback: wrap as {text: ...} so tests can still see it.
				return map[string]any{"text": text}, nil
			}
		}
	}
	return out.Result, nil
}

// GET issues an authenticated GET against the sidecar. If out is
// non-nil, the response body is JSON-decoded into it.
func (s *Sidecar) GET(path string, out any) *Response {
	return s.requestJSON("GET", path, nil, out)
}

// POST issues an authenticated POST with a JSON body. Pass nil body
// to send an empty payload.
func (s *Sidecar) POST(path string, body, out any) *Response {
	return s.requestJSON("POST", path, body, out)
}

// PATCH — same shape as POST.
func (s *Sidecar) PATCH(path string, body, out any) *Response {
	return s.requestJSON("PATCH", path, body, out)
}

// DELETE — no body, returns the raw response.
func (s *Sidecar) DELETE(path string) *Response {
	return s.requestJSON("DELETE", path, nil, nil)
}

// Response is the small wrapper helpers return so tests can assert
// status codes without the http.Response noise.
type Response struct {
	Status int
	Body   []byte
}

func (s *Sidecar) requestJSON(method, path string, body, out any) *Response {
	s.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	resp, err := s.do(method, path, reader, "application/json")
	if err != nil {
		s.t.Fatalf("testkit: %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			s.t.Fatalf("testkit: decode %s %s (status %d): %v\nbody: %s",
				method, path, resp.StatusCode, err, string(raw))
		}
	}
	return &Response{Status: resp.StatusCode, Body: raw}
}

func (s *Sidecar) do(method, path string, body io.Reader, contentType string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = ctx
	req, err := http.NewRequest(method, s.url+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" && body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	return http.DefaultClient.Do(req)
}

// ─── Build cache + helpers ────────────────────────────────────────

var (
	buildCacheMu sync.Mutex
	buildCache   = map[string]string{} // appDir → binary path
)

// buildSidecar runs `go build` once per appDir per test process and
// returns the cached binary path. A second call for the same appDir
// in the same `go test` run is a no-op.
func buildSidecar(t *testing.T, appDir string) string {
	t.Helper()
	abs, err := filepath.Abs(appDir)
	if err != nil {
		t.Fatalf("testkit: abs %q: %v", appDir, err)
	}
	buildCacheMu.Lock()
	defer buildCacheMu.Unlock()
	if bin, ok := buildCache[abs]; ok {
		return bin
	}
	tmp, err := os.CreateTemp("", "testkit-bin-*")
	if err != nil {
		t.Fatalf("testkit: temp bin: %v", err)
	}
	tmp.Close()
	out := tmp.Name()
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = abs
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("testkit: go build %s: %v", abs, err)
	}
	buildCache[abs] = out
	return out
}

func pickFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("testkit: pick port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitHealthy(url string, deadline time.Duration) error {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("not healthy after %s", deadline)
}

// lockedBuffer is io.Writer + Stringer with a mutex. The cmd.Stdout
// writes happen in a goroutine; reads from the test goroutine need
// the lock to avoid the race.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
