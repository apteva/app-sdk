package sdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// httpPlatformClient is the SDK's default PlatformClient — talks to
// apteva-server's /api/apps/callback/* surface using the per-install
// APTEVA_APP_TOKEN. The platform validates the token + checks the
// caller's declared permissions on every call.
//
// Two HTTP clients: `client` for fast metadata endpoints (whoami,
// connection lookup, event send) at 30s; `slowClient` for callbacks
// that proxy upstream API calls or other apps' tools, where the wire
// time is bounded by the platform's own per-tool timeout — generous
// here, restrictive at the next hop.
type httpPlatformClient struct {
	baseURL    string
	token      string
	client     *http.Client
	slowClient *http.Client

	// platform_info cache. Sidecars used to read public_url etc. via
	// the APTEVA_PUBLIC_URL env var captured at spawn time; that meant
	// operators changing public_url in settings had to restart every
	// sidecar. PlatformInfo() now fetches a small JSON blob from the
	// platform and caches it for piCacheTTL, so apps get hot updates
	// within a minute of an operator change without DOSing the
	// server from hot loops.
	piMu       sync.Mutex
	piCached   *PlatformInfo
	piCachedAt time.Time
}

const piCacheTTL = 60 * time.Second

func newHTTPPlatformClient(baseURL, token string) PlatformClient {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:5280"
	}
	return &httpPlatformClient{
		baseURL: baseURL, token: token,
		client:     &http.Client{Timeout: 30 * time.Second},
		slowClient: &http.Client{Timeout: 10 * time.Minute},
	}
}

// PlatformInfo fetches the platform-level facts bag (public_url +
// version). 60-second in-memory cache so apps can call from hot loops.
// Older platforms that don't yet expose /api/apps/callback/platform-info
// return a 404; we degrade gracefully by reading APTEVA_PUBLIC_URL from
// the env (the legacy spawn-time variable) so apps that switch to this
// helper keep working against pre-v0.17.6 servers.
func (c *httpPlatformClient) PlatformInfo() (*PlatformInfo, error) {
	c.piMu.Lock()
	if c.piCached != nil && time.Since(c.piCachedAt) < piCacheTTL {
		out := *c.piCached
		c.piMu.Unlock()
		return &out, nil
	}
	c.piMu.Unlock()

	var fresh PlatformInfo
	err := c.get("/api/apps/callback/platform-info", &fresh)
	if err != nil {
		// Back-compat: fall back to the env. Don't cache the env-fallback
		// (the platform might come online with the endpoint between calls
		// — let the next call retry the real fetch).
		if envURL := strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_URL")); envURL != "" {
			return &PlatformInfo{PublicURL: envURL}, nil
		}
		return nil, err
	}

	c.piMu.Lock()
	c.piCached = &fresh
	c.piCachedAt = time.Now()
	c.piMu.Unlock()
	return &fresh, nil
}

func (c *httpPlatformClient) GetConnection(id int64) (*PlatformConnection, error) {
	var out PlatformConnection
	if err := c.get("/api/apps/callback/connections/"+strconv.FormatInt(id, 10), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ListConnections(filter ConnectionFilter) ([]PlatformConnection, error) {
	q := ""
	if filter.ProjectID != "" {
		q = "?project_id=" + filter.ProjectID
	}
	if filter.AppSlug != "" {
		if q == "" {
			q = "?"
		} else {
			q += "&"
		}
		q += "app_slug=" + filter.AppSlug
	}
	var out []PlatformConnection
	if err := c.get("/api/apps/callback/connections"+q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *httpPlatformClient) GetInstance(id int64) (*PlatformInstance, error) {
	// Hit the canonical /agents/<id> callback path; the server's
	// Phase 2 alias makes /instances/<id> work just as well, but new
	// SDK code uses the new vocabulary.
	var out PlatformInstance
	if err := c.get("/api/apps/callback/agents/"+strconv.FormatInt(id, 10), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetAgent is the agent-vocabulary alias for GetInstance. Both methods
// return the same PlatformInstance / PlatformAgent struct (type alias)
// and hit the same platform endpoint. New code should prefer GetAgent.
func (c *httpPlatformClient) GetAgent(id int64) (*PlatformAgent, error) {
	return c.GetInstance(id)
}

func (c *httpPlatformClient) SendEvent(instanceID int64, message string) error {
	return c.post("/api/apps/callback/agents/"+strconv.FormatInt(instanceID, 10)+"/event",
		map[string]any{"message": message}, nil)
}

func (c *httpPlatformClient) SendToChannel(channelName, projectID, message string) error {
	return c.post("/api/apps/callback/channels/send",
		map[string]any{"channel": channelName, "project_id": projectID, "message": message}, nil)
}

func (c *httpPlatformClient) WhoAmI() (*InstallIdentity, error) {
	var out InstallIdentity
	if err := c.get("/api/apps/callback/whoami", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*ExecuteResult, error) {
	if input == nil {
		input = map[string]any{}
	}
	body := map[string]any{"tool": tool, "input": input}
	var out ExecuteResult
	if err := c.postWith(c.slowClient, "/api/apps/callback/integrations/"+strconv.FormatInt(connID, 10)+"/execute", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	if input == nil {
		input = map[string]any{}
	}
	body := map[string]any{"tool": tool, "input": input}
	var out json.RawMessage
	if err := c.postWith(c.slowClient, "/api/apps/callback/apps/"+appName+"/call", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CallAppResult — CallApp + MCP-envelope unwrap. See PlatformClient
// interface doc for the wire shape and motivation. Pure addition;
// CallApp keeps its raw-envelope contract for the apps that already
// strip the envelope themselves (deploy/domain_link, certs/domain_link).
func (c *httpPlatformClient) CallAppResult(appName, tool string, input map[string]any, out any) error {
	raw, err := c.CallApp(appName, tool, input)
	if err != nil {
		return err
	}
	return decodeMCPEnvelope(raw, appName, tool, out)
}

// decodeMCPEnvelope strips the JSON-RPC + content-array layers and
// json.Unmarshals the inner text into out. Resilient to two
// shapes:
//
//  1. Full envelope: {"jsonrpc":..., "result":{"content":[{"text":"<json>"}]}}
//  2. Bare inner: <json> directly (test mocks, future platform
//     versions that pre-unwrap, etc.)
//
// On RPC-level error (.error.code) returns a descriptive Go error
// without ever touching out.
func decodeMCPEnvelope(raw json.RawMessage, appName, tool string, out any) error {
	if out == nil {
		return errors.New("CallAppResult: out is nil")
	}
	if len(raw) == 0 {
		return fmt.Errorf("%s.%s: empty response", appName, tool)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	// Try the envelope shape; if it doesn't fit (caller is using a
	// pre-unwrapped client or the bytes are already the inner JSON),
	// fall through to direct decode.
	if err := json.Unmarshal(raw, &env); err != nil {
		return json.Unmarshal(raw, out)
	}
	if env.Error != nil {
		return fmt.Errorf("%s.%s: %s (code=%d)", appName, tool, env.Error.Message, env.Error.Code)
	}
	if len(env.Result) > 0 {
		if handled, err := decodeMCPContent(env.Result, appName, tool, out); handled || err != nil {
			return err
		}
	}
	// Environment seed/app-call endpoints return the MCP result object
	// directly ({content:[...]}) rather than the full JSON-RPC envelope.
	if handled, err := decodeMCPContent(raw, appName, tool, out); handled || err != nil {
		return err
	}
	// Either the response was already unwrapped (some test paths) or it
	// had no content. Try direct decode; if that fails we surface the
	// original bytes for diagnosis.
	if jerr := json.Unmarshal(raw, out); jerr != nil {
		return fmt.Errorf("%s.%s: response had no content array and direct decode failed: %w", appName, tool, jerr)
	}
	return nil
}

func decodeMCPContent(raw json.RawMessage, appName, tool string, out any) (bool, error) {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError,omitempty"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Content) == 0 {
		return false, nil
	}
	inner := result.Content[0].Text
	if inner == "" {
		return true, fmt.Errorf("%s.%s: empty content text", appName, tool)
	}
	if result.IsError {
		return true, fmt.Errorf("%s.%s: tool returned error: %.200s", appName, tool, inner)
	}
	if err := json.Unmarshal([]byte(inner), out); err != nil {
		return true, fmt.Errorf("%s.%s: decode inner JSON: %w (text: %.200s)", appName, tool, err, inner)
	}
	return true, nil
}

func (c *httpPlatformClient) StartOAuth(req OAuthStartRequest) (*OAuthStartResult, error) {
	var out OAuthStartResult
	if err := c.post("/api/apps/callback/oauth/start", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) DisconnectConnection(connID int64) error {
	return c.post("/api/apps/callback/connections/"+strconv.FormatInt(connID, 10)+"/disconnect", nil, nil)
}

func (c *httpPlatformClient) ListOwnedConnections() ([]PlatformConnection, error) {
	var out []PlatformConnection
	if err := c.get("/api/apps/callback/connections?owned=true", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetGrants fetches the policy the operator wrote for (this install,
// instanceID). Returns empty rules + default "allow" when the
// platform endpoint is missing — back-compat with older servers
// that haven't shipped the grants API yet.
func (c *httpPlatformClient) GetGrants(instanceID int64) (*GrantsResponse, error) {
	var out GrantsResponse
	path := "/api/apps/callback/grants?instance_id=" + strconv.FormatInt(instanceID, 10)
	err := c.get(path, &out)
	if err != nil {
		// Older servers return 404 for the endpoint; treat as
		// "default allow, no rules" so existing apps don't 403
		// after upgrading the SDK.
		return &GrantsResponse{DefaultEffect: "allow"}, nil
	}
	if out.DefaultEffect == "" {
		out.DefaultEffect = "allow"
	}
	return &out, nil
}

// GetConnectionCredentials hits /api/apps/callback/connections/:id/credentials.
// Authorization (permission, compatible_slugs, binding) is enforced
// server-side; this is a thin GET wrapper.
func (c *httpPlatformClient) GetConnectionCredentials(id int64) (*ConnectionCredentials, error) {
	var out ConnectionCredentials
	path := "/api/apps/callback/connections/" + strconv.FormatInt(id, 10) + "/credentials"
	if err := c.get(path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ListProjects() ([]PlatformProject, error) {
	var out []PlatformProject
	if err := c.get("/api/apps/callback/projects", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *httpPlatformClient) ExposeIngress(req IngressExposeRequest) (*IngressRoute, error) {
	var out struct {
		Route IngressRoute `json:"route"`
	}
	if err := c.post("/api/apps/callback/ingress/expose", req, &out); err != nil {
		return nil, err
	}
	return &out.Route, nil
}

func (c *httpPlatformClient) UnexposeIngress(hostname string) error {
	if strings.TrimSpace(hostname) == "" {
		return errors.New("UnexposeIngress: hostname required")
	}
	return c.post("/api/apps/callback/ingress/unexpose", map[string]any{"hostname": hostname}, nil)
}

func (c *httpPlatformClient) ListIngressRoutes() ([]IngressRoute, error) {
	var out struct {
		Routes []IngressRoute `json:"routes"`
	}
	if err := c.get("/api/apps/callback/ingress/routes", &out); err != nil {
		return nil, err
	}
	if out.Routes == nil {
		out.Routes = []IngressRoute{}
	}
	return out.Routes, nil
}

func (c *httpPlatformClient) ListDomainGrants() ([]DomainGrant, error) {
	var out struct {
		Grants []DomainGrant `json:"grants"`
	}
	if err := c.get("/api/apps/callback/dns/grants", &out); err != nil {
		return nil, err
	}
	if out.Grants == nil {
		out.Grants = []DomainGrant{}
	}
	return out.Grants, nil
}

func (c *httpPlatformClient) UpsertDNSRecord(req DNSRecordRequest) (*DNSRecordResult, error) {
	var out DNSRecordResult
	if err := c.post("/api/apps/callback/dns/records", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) DeleteDNSRecord(req DNSRecordRequest) (*DNSRecordResult, error) {
	var out DNSRecordResult
	if err := c.deleteWithBody("/api/apps/callback/dns/records", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ListEnvironments() ([]EnvironmentSummary, error) {
	var out []EnvironmentSummary
	if err := c.get("/api/environments", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *httpPlatformClient) CreateEnvironment(req EnvironmentCreateRequest) (*EnvironmentSummary, error) {
	var out EnvironmentSummary
	if err := c.postWith(c.slowClient, "/api/environments", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) GetEnvironment(id string) (*EnvironmentSummary, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("GetEnvironment: id required")
	}
	var out EnvironmentSummary
	if err := c.get("/api/environments/"+url.PathEscape(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) DestroyEnvironment(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("DestroyEnvironment: id required")
	}
	return c.delete("/api/environments/"+url.PathEscape(id), nil)
}

func (c *httpPlatformClient) SeedEnvironment(id string, calls []EnvironmentSeedCall, seedBaseDir string) ([]json.RawMessage, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("SeedEnvironment: id required")
	}
	if len(calls) == 0 {
		return []json.RawMessage{}, nil
	}
	body := map[string]any{"calls": calls}
	if seedBaseDir != "" {
		body["seed_base_dir"] = seedBaseDir
	}
	var out struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := c.postWith(c.slowClient, "/api/environments/"+url.PathEscape(id)+"/seed", body, &out); err != nil {
		return nil, err
	}
	if out.Results == nil {
		out.Results = []json.RawMessage{}
	}
	return out.Results, nil
}

func (c *httpPlatformClient) CallEnvironmentApp(environmentID, appName, tool string, input map[string]any) (json.RawMessage, error) {
	if strings.TrimSpace(appName) == "" {
		return nil, errors.New("CallEnvironmentApp: appName required")
	}
	if strings.TrimSpace(tool) == "" {
		return nil, errors.New("CallEnvironmentApp: tool required")
	}
	results, err := c.SeedEnvironment(environmentID, []EnvironmentSeedCall{{App: appName, Tool: tool, Input: input}}, "")
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("CallEnvironmentApp %s.%s: empty response", appName, tool)
	}
	return results[0], nil
}

func (c *httpPlatformClient) CallEnvironmentAppResult(environmentID, appName, tool string, input map[string]any, out any) error {
	raw, err := c.CallEnvironmentApp(environmentID, appName, tool, input)
	if err != nil {
		return err
	}
	return decodeMCPEnvelope(raw, appName, tool, out)
}

func (c *httpPlatformClient) SnapshotEnvironment(environmentID string, req EnvironmentSnapshotRequest) (*EnvironmentSnapshot, error) {
	if strings.TrimSpace(environmentID) == "" {
		return nil, errors.New("SnapshotEnvironment: environmentID required")
	}
	var out EnvironmentSnapshot
	if err := c.postWith(c.slowClient, "/api/environments/"+url.PathEscape(environmentID)+"/snapshot", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ListEnvironmentAgents(environmentID string) ([]EnvironmentAgent, error) {
	if strings.TrimSpace(environmentID) == "" {
		return nil, errors.New("ListEnvironmentAgents: environmentID required")
	}
	var out []EnvironmentAgent
	if err := c.get("/api/environments/"+url.PathEscape(environmentID)+"/agents", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *httpPlatformClient) SpawnEnvironmentAgent(environmentID string, req EnvironmentAgentSpawnRequest) (*EnvironmentAgent, error) {
	if strings.TrimSpace(environmentID) == "" {
		return nil, errors.New("SpawnEnvironmentAgent: environmentID required")
	}
	if req.SourceAgentID == 0 {
		return nil, errors.New("SpawnEnvironmentAgent: source_agent_id required")
	}
	var out EnvironmentAgent
	if err := c.postWith(c.slowClient, "/api/environments/"+url.PathEscape(environmentID)+"/agents", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) StopEnvironmentAgent(environmentID string, agentOrAlias string) error {
	if strings.TrimSpace(environmentID) == "" {
		return errors.New("StopEnvironmentAgent: environmentID required")
	}
	path := "/api/environments/" + url.PathEscape(environmentID) + "/agent"
	if strings.TrimSpace(agentOrAlias) != "" {
		path = "/api/environments/" + url.PathEscape(environmentID) + "/agents/" + url.PathEscape(agentOrAlias)
	}
	return c.delete(path, nil)
}

// SpawnRealtimeThread hits /api/apps/callback/threads/spawn-realtime.
// Returns the spawn result including audio_bridge_url + audio_token
// the caller uses to dial core's audio WebSocket.
func (c *httpPlatformClient) SpawnRealtimeThread(req RealtimeSpawnRequest) (*RealtimeSpawnResult, error) {
	if req.AgentID == 0 {
		return nil, errors.New("SpawnRealtimeThread: agent_id required (pull from CallerFrom(ctx).AgentID in HandlerCtx)")
	}
	if req.ThreadID == "" {
		return nil, errors.New("SpawnRealtimeThread: thread_id required")
	}
	if req.Directive == "" {
		return nil, errors.New("SpawnRealtimeThread: directive required")
	}
	var out RealtimeSpawnResult
	if err := c.post("/api/apps/callback/threads/spawn-realtime", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// KillThread hits /api/apps/callback/threads/{id} with DELETE.
// Idempotent — 404 on unknown id is treated as success because the
// caller's intent (no live thread by this name) is already satisfied.
func (c *httpPlatformClient) KillThread(threadID string) error {
	if threadID == "" {
		return errors.New("KillThread: thread_id required")
	}
	req, err := http.NewRequest("DELETE", c.baseURL+"/api/apps/callback/threads/"+threadID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("KillThread %s: %d %s", threadID, resp.StatusCode, string(body))
	}
	return nil
}

// projectScopedClient wraps a PlatformClient so CallApp / CallAppResult
// auto-thread `_project_id` into the input map when the caller hasn't
// supplied one. Used by AppCtx.WithProject — apps' worker code calling
// `ctx.PlatformAPI().CallAppResult("storage", "files_list", args, &out)`
// from a global install thus carries the dispatcher's current project
// without anyone touching the call site.
//
// Every other method passes straight through — the project hint
// affects only the two app-to-app entry points, which are the only
// surface where downstream code needs to know which project the call
// is on behalf of.
type projectScopedClient struct {
	inner     PlatformClient
	projectID string
}

func wrapPlatformWithProject(inner PlatformClient, projectID string) PlatformClient {
	if projectID == "" || inner == nil {
		return inner
	}
	// Avoid re-wrapping if the inner client already has the same
	// project pinned — idempotent under repeated WithProject calls.
	if existing, ok := inner.(*projectScopedClient); ok && existing.projectID == projectID {
		return existing
	}
	return &projectScopedClient{inner: inner, projectID: projectID}
}

func (p *projectScopedClient) withProject(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{"_project_id": p.projectID}
	}
	if _, set := input["_project_id"]; set {
		return input
	}
	cp := make(map[string]any, len(input)+1)
	for k, v := range input {
		cp[k] = v
	}
	cp["_project_id"] = p.projectID
	return cp
}

func (p *projectScopedClient) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	return p.inner.CallApp(appName, tool, p.withProject(input))
}

func (p *projectScopedClient) CallAppResult(appName, tool string, input map[string]any, out any) error {
	return p.inner.CallAppResult(appName, tool, p.withProject(input), out)
}

// Pass-throughs for every other method.

func (p *projectScopedClient) GetConnection(id int64) (*PlatformConnection, error) {
	return p.inner.GetConnection(id)
}
func (p *projectScopedClient) ListConnections(f ConnectionFilter) ([]PlatformConnection, error) {
	return p.inner.ListConnections(f)
}
func (p *projectScopedClient) GetInstance(id int64) (*PlatformInstance, error) {
	return p.inner.GetInstance(id)
}
func (p *projectScopedClient) GetAgent(id int64) (*PlatformAgent, error) {
	return p.inner.GetAgent(id)
}
func (p *projectScopedClient) SendEvent(instanceID int64, message string) error {
	return p.inner.SendEvent(instanceID, message)
}
func (p *projectScopedClient) SendToChannel(channelName, projectID, message string) error {
	if projectID == "" {
		projectID = p.projectID
	}
	return p.inner.SendToChannel(channelName, projectID, message)
}
func (p *projectScopedClient) WhoAmI() (*InstallIdentity, error) {
	id, err := p.inner.WhoAmI()
	if err != nil || id == nil || strings.TrimSpace(p.projectID) == "" {
		return id, err
	}
	scoped := *id
	scoped.ProjectID = p.projectID
	if projects, perr := p.inner.ListProjects(); perr == nil {
		for _, project := range projects {
			if project.ID == p.projectID {
				scoped.ProjectName = project.Name
				scoped.ProjectDescription = project.Description
				break
			}
		}
	}
	return &scoped, nil
}
func (p *projectScopedClient) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*ExecuteResult, error) {
	return p.inner.ExecuteIntegrationTool(connID, tool, input)
}
func (p *projectScopedClient) StartOAuth(req OAuthStartRequest) (*OAuthStartResult, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return p.inner.StartOAuth(req)
}
func (p *projectScopedClient) DisconnectConnection(connID int64) error {
	return p.inner.DisconnectConnection(connID)
}
func (p *projectScopedClient) ListOwnedConnections() ([]PlatformConnection, error) {
	return p.inner.ListOwnedConnections()
}
func (p *projectScopedClient) GetGrants(instanceID int64) (*GrantsResponse, error) {
	return p.inner.GetGrants(instanceID)
}
func (p *projectScopedClient) GetConnectionCredentials(id int64) (*ConnectionCredentials, error) {
	return p.inner.GetConnectionCredentials(id)
}
func (p *projectScopedClient) ListProjects() ([]PlatformProject, error) {
	return p.inner.ListProjects()
}
func (p *projectScopedClient) ExposeIngress(req IngressExposeRequest) (*IngressRoute, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return p.inner.ExposeIngress(req)
}
func (p *projectScopedClient) UnexposeIngress(hostname string) error {
	return p.inner.UnexposeIngress(hostname)
}
func (p *projectScopedClient) ListIngressRoutes() ([]IngressRoute, error) {
	return p.inner.ListIngressRoutes()
}
func (p *projectScopedClient) ListDomainGrants() ([]DomainGrant, error) {
	return p.inner.ListDomainGrants()
}
func (p *projectScopedClient) UpsertDNSRecord(req DNSRecordRequest) (*DNSRecordResult, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return p.inner.UpsertDNSRecord(req)
}
func (p *projectScopedClient) DeleteDNSRecord(req DNSRecordRequest) (*DNSRecordResult, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return p.inner.DeleteDNSRecord(req)
}
func (p *projectScopedClient) SpawnRealtimeThread(req RealtimeSpawnRequest) (*RealtimeSpawnResult, error) {
	return p.inner.SpawnRealtimeThread(req)
}
func (p *projectScopedClient) KillThread(threadID string) error {
	return p.inner.KillThread(threadID)
}
func (p *projectScopedClient) PlatformInfo() (*PlatformInfo, error) {
	return p.inner.PlatformInfo()
}
func (p *projectScopedClient) ListEnvironments() ([]EnvironmentSummary, error) {
	return p.inner.ListEnvironments()
}
func (p *projectScopedClient) CreateEnvironment(req EnvironmentCreateRequest) (*EnvironmentSummary, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return p.inner.CreateEnvironment(req)
}
func (p *projectScopedClient) GetEnvironment(id string) (*EnvironmentSummary, error) {
	return p.inner.GetEnvironment(id)
}
func (p *projectScopedClient) DestroyEnvironment(id string) error {
	return p.inner.DestroyEnvironment(id)
}
func (p *projectScopedClient) SeedEnvironment(id string, calls []EnvironmentSeedCall, seedBaseDir string) ([]json.RawMessage, error) {
	return p.inner.SeedEnvironment(id, calls, seedBaseDir)
}
func (p *projectScopedClient) CallEnvironmentApp(environmentID, appName, tool string, input map[string]any) (json.RawMessage, error) {
	return p.inner.CallEnvironmentApp(environmentID, appName, tool, input)
}
func (p *projectScopedClient) CallEnvironmentAppResult(environmentID, appName, tool string, input map[string]any, out any) error {
	return p.inner.CallEnvironmentAppResult(environmentID, appName, tool, input, out)
}
func (p *projectScopedClient) SnapshotEnvironment(environmentID string, req EnvironmentSnapshotRequest) (*EnvironmentSnapshot, error) {
	return p.inner.SnapshotEnvironment(environmentID, req)
}
func (p *projectScopedClient) ListEnvironmentAgents(environmentID string) ([]EnvironmentAgent, error) {
	return p.inner.ListEnvironmentAgents(environmentID)
}
func (p *projectScopedClient) SpawnEnvironmentAgent(environmentID string, req EnvironmentAgentSpawnRequest) (*EnvironmentAgent, error) {
	return p.inner.SpawnEnvironmentAgent(environmentID, req)
}
func (p *projectScopedClient) StopEnvironmentAgent(environmentID string, agentOrAlias string) error {
	return p.inner.StopEnvironmentAgent(environmentID, agentOrAlias)
}

// --- low-level helpers -------------------------------------------------------

func (c *httpPlatformClient) get(path string, out any) error {
	req, _ := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return c.platformErr(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *httpPlatformClient) post(path string, body any, out any) error {
	return c.postWith(c.client, path, body, out)
}

func (c *httpPlatformClient) postWith(client *http.Client, path string, body any, out any) error {
	var br io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		br = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(http.MethodPost, c.baseURL+path, br)
	req.Header.Set("Content-Type", "application/json")
	c.addAuth(req)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return c.platformErr(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *httpPlatformClient) put(path string, body any, out any) error {
	var br io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		br = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(http.MethodPut, c.baseURL+path, br)
	req.Header.Set("Content-Type", "application/json")
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return c.platformErr(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *httpPlatformClient) deleteWithBody(path string, body any, out any) error {
	var br io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		br = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(http.MethodDelete, c.baseURL+path, br)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return c.platformErr(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *httpPlatformClient) delete(path string, out any) error {
	req, _ := http.NewRequest(http.MethodDelete, c.baseURL+path, nil)
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return c.platformErr(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *httpPlatformClient) addAuth(req *http.Request) {
	if c.token == "" {
		c.token = os.Getenv("APTEVA_APP_TOKEN")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("X-Apteva-App-Install-ID", os.Getenv("APTEVA_INSTALL_ID"))
	// When this sidecar runs inside a test Environment, forward the
	// environment id so the platform routes its integration calls to that
	// environment's interceptor. Empty/unset in production is a no-op.
	if eid := os.Getenv("APTEVA_ENVIRONMENT_ID"); eid != "" {
		req.Header.Set("X-Apteva-Environment-Id", eid)
	}
}

func (c *httpPlatformClient) platformErr(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("platform %s: http %d: %s", resp.Request.URL.Path, resp.StatusCode, string(body))
}
