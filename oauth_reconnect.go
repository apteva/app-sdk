package sdk

import "errors"

// OAuthReconnectIdentity identifies a destination bound to the connection.
type OAuthReconnectIdentity struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// OAuthReconnectRequest reauthorizes an existing app-owned OAuth connection.
type OAuthReconnectRequest struct {
	ConnectionID       int64                    `json:"connection_id"`
	ExpectedIdentities []OAuthReconnectIdentity `json:"expected_identities"`
	ProjectID          string                   `json:"project_id,omitempty"`
}

type OAuthReconnectQuery struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id,omitempty"`
	Cancel    bool   `json:"cancel,omitempty"`
}

type OAuthReconnectResult struct {
	ID           string `json:"id"`
	ConnectionID int64  `json:"connection_id"`
	Status       string `json:"status"`
	AuthorizeURL string `json:"authorize_url,omitempty"`
	ExpiresAt    string `json:"expires_at"`
}

type OAuthReconnectClient interface {
	StartOAuthReconnect(OAuthReconnectRequest) (*OAuthReconnectResult, error)
	OAuthReconnectStatus(OAuthReconnectQuery) (*OAuthReconnectResult, error)
}

func StartOAuthReconnect(client PlatformClient, req OAuthReconnectRequest) (*OAuthReconnectResult, error) {
	reauth, ok := client.(OAuthReconnectClient)
	if !ok {
		return nil, errors.New("platform does not support verified OAuth reconnect")
	}
	return reauth.StartOAuthReconnect(req)
}

func OAuthReconnectStatus(client PlatformClient, req OAuthReconnectQuery) (*OAuthReconnectResult, error) {
	reauth, ok := client.(OAuthReconnectClient)
	if !ok {
		return nil, errors.New("platform does not support verified OAuth reconnect")
	}
	return reauth.OAuthReconnectStatus(req)
}

func (c *httpPlatformClient) StartOAuthReconnect(req OAuthReconnectRequest) (*OAuthReconnectResult, error) {
	var out OAuthReconnectResult
	if err := c.post("/api/apps/callback/oauth/reconnect", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) OAuthReconnectStatus(req OAuthReconnectQuery) (*OAuthReconnectResult, error) {
	var out OAuthReconnectResult
	if err := c.post("/api/apps/callback/oauth/reconnect/status", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (p *projectScopedClient) StartOAuthReconnect(req OAuthReconnectRequest) (*OAuthReconnectResult, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return StartOAuthReconnect(p.inner, req)
}

func (p *projectScopedClient) OAuthReconnectStatus(req OAuthReconnectQuery) (*OAuthReconnectResult, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return OAuthReconnectStatus(p.inner, req)
}
