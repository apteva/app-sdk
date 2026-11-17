// apteva-app-contract is offline manifest tooling and an explicitly targeted
// HTTP conformance checker. It never discovers or contacts an installed Server.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/contract"
	"github.com/apteva/app-sdk/internal/manifestschema"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: apteva-app-contract schema | validate MANIFEST | check --url URL [--fixtures FILE] (token from APTEVA_APP_TOKEN)")
	}
	switch os.Args[1] {
	case "schema":
		e := json.NewEncoder(os.Stdout)
		e.SetIndent("", "  ")
		return e.Encode(manifestschema.Generate())
	case "validate":
		if len(os.Args) != 3 {
			return fmt.Errorf("validate requires a manifest path")
		}
		b, e := os.ReadFile(os.Args[2])
		if e != nil {
			return e
		}
		m, e := sdk.ParseManifest(b)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(m)
	case "check":
		f := flag.NewFlagSet("check", flag.ContinueOnError)
		base := f.String("url", "", "explicit app endpoint")
		extra := f.String("fixtures", "", "additional safe test requests")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		if *base == "" {
			return fmt.Errorf("--url is required")
		}
		fixtures := contract.BasicFixtures()
		if *extra != "" {
			b, e := os.ReadFile(*extra)
			if e != nil {
				return e
			}
			var more []contract.Fixture
			if e = json.Unmarshal(b, &more); e != nil {
				return e
			}
			fixtures = append(fixtures, more...)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		token := os.Getenv("APTEVA_APP_TOKEN")
		if e := contract.Check(ctx, *base, token, fixtures); e != nil {
			return e
		}
		req, e := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(*base, "/")+"/manifest", nil)
		if e != nil {
			return e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, e := client.Do(req)
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if e != nil {
			return e
		}
		if _, e = sdk.ParseManifest(raw); e != nil {
			return e
		}
		fmt.Printf("Passed %d HTTP/MCP checks and manifest validation\n", len(fixtures))
		return nil
	default:
		return fmt.Errorf("unknown command")
	}
}
