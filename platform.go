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
type httpPlatformClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func newHTTPPlatformClient(baseURL, token string) PlatformClient {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:5280"
	}
	return &httpPlatformClient{
		baseURL: baseURL, token: token,
		client: &http.Client{Timeout: 30 * time.Second},
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
}

func (c *httpPlatformClient) platformErr(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("platform %s: http %d: %s", resp.Request.URL.Path, resp.StatusCode, string(body))
}
