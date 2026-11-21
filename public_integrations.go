package sdk

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// PublicIntegrationClient runs tools of keyless catalog integrations
// (auth type "none") by slug, without a connection. It is optional so
// existing PlatformClient implementations and test stubs stay source
// compatible; call it through ExecutePublicIntegrationTool.
//
// The platform executes the catalog tool exactly as it would for a
// connection (pacing, timeouts, retries, usage), but sends no
// credentials. Integrations that need credentials are refused; those
// still go through a connection and ExecuteIntegrationTool.
//
// Requires the connections.execute permission.
type PublicIntegrationClient interface {
	ExecutePublicIntegrationToolContext(ctx context.Context, slug, tool string, input map[string]any) (*ExecuteResult, error)
}

// ErrPublicIntegrationsUnavailable means the platform client does not
// support keyless integration calls (an older platform or a test stub).
var ErrPublicIntegrationsUnavailable = errors.New("public integration API unavailable")

// ExecutePublicIntegrationTool runs a keyless integration's tool by slug.
func ExecutePublicIntegrationTool(ctx context.Context, client PlatformClient, slug, tool string, input map[string]any) (*ExecuteResult, error) {
	if api, ok := client.(PublicIntegrationClient); ok {
		return api.ExecutePublicIntegrationToolContext(ctx, slug, tool, input)
	}
	return nil, ErrPublicIntegrationsUnavailable
}

func (c *httpPlatformClient) ExecutePublicIntegrationToolContext(ctx context.Context, slug, tool string, input map[string]any) (*ExecuteResult, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil, errors.New("integration slug required")
	}
	if input == nil {
		input = map[string]any{}
	}
	body := map[string]any{"tool": tool, "input": input}
	var out ExecuteResult
	if err := c.postWithContext(ctx, c.slowClient, "/api/apps/callback/integrations/public/"+url.PathEscape(slug)+"/execute", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (p *projectScopedClient) ExecutePublicIntegrationToolContext(ctx context.Context, slug, tool string, input map[string]any) (*ExecuteResult, error) {
	return ExecutePublicIntegrationTool(ctx, p.inner, slug, tool, input)
}
