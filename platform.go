package sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
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
}

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
	var out PlatformInstance
	if err := c.get("/api/apps/callback/instances/"+strconv.FormatInt(id, 10), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) SendEvent(instanceID int64, message string) error {
	return c.post("/api/apps/callback/instances/"+strconv.FormatInt(instanceID, 10)+"/event",
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

func (c *httpPlatformClient) addAuth(req *http.Request) {
	if c.token == "" {
		c.token = os.Getenv("APTEVA_APP_TOKEN")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("X-Apteva-App-Install-ID", os.Getenv("APTEVA_INSTALL_ID"))
}

func (c *httpPlatformClient) platformErr(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("platform %s: http %d: %s", resp.Request.URL.Path, resp.StatusCode, string(body))
}
