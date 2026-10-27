// Package contract checks the existing app HTTP/MCP boundary, independent of
// implementation language. It never installs apps or changes platform state.
package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Fixture struct {
	Name   string          `json:"name"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Auth   string          `json:"auth"`
	Body   json.RawMessage `json:"body"`
	Status int             `json:"status"`
	Want   json.RawMessage `json:"want"`
}

// Check sends only the supplied requests. Tool fixtures must be safe test tools.
// Object expectations are subsets; arrays and scalar values compare exactly.
func Check(ctx context.Context, base, token string, fixtures []Fixture) error {
	if token == "" {
		return fmt.Errorf("a test installation token is required")
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, f := range fixtures {
		if !strings.HasPrefix(f.Path, "/") || strings.HasPrefix(f.Path, "//") {
			return fmt.Errorf("invalid fixture path")
		}
		req, e := http.NewRequestWithContext(ctx, f.Method, strings.TrimRight(base, "/")+f.Path, bytes.NewReader(f.Body))
		if e != nil {
			return e
		}
		switch f.Auth {
		case "valid":
			req.Header.Set("Authorization", "Bearer "+token)
		case "invalid":
			req.Header.Set("Authorization", "Bearer invalid-"+token)
		case "none":
		default:
			return fmt.Errorf("%s: unknown fixture auth mode", f.Name)
		}
		if len(f.Body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, e := client.Do(req)
		if e != nil {
			return fmt.Errorf("%s: request failed: %w", f.Name, e)
		}
		raw, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if e != nil {
			return e
		}
		if resp.StatusCode != f.Status {
			return fmt.Errorf("%s: HTTP %d, expected %d", f.Name, resp.StatusCode, f.Status)
		}
		if len(f.Want) > 0 {
			var got, want any
			if json.Unmarshal(raw, &got) != nil || json.Unmarshal(f.Want, &want) != nil {
				return fmt.Errorf("%s: invalid JSON", f.Name)
			}
			if !contains(got, want) {
				return fmt.Errorf("%s: response does not match contract", f.Name)
			}
		}
	}
	return nil
}
func contains(got, want any) bool {
	if obj, ok := want.(map[string]any); ok {
		actual, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range obj {
			a, exists := actual[k]
			if !exists || !contains(a, v) {
				return false
			}
		}
		return true
	}
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	return bytes.Equal(a, b)
}
