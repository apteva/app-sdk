package sdk

import (
	"encoding/json"
	"testing"
)

type stubProjectPlatformClient struct {
	identity *InstallIdentity
	projects []PlatformProject
}

func (s *stubProjectPlatformClient) GetConnection(int64) (*PlatformConnection, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) ListConnections(ConnectionFilter) ([]PlatformConnection, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) GetInstance(int64) (*PlatformInstance, error) { return nil, nil }
func (s *stubProjectPlatformClient) GetAgent(int64) (*PlatformAgent, error)       { return nil, nil }
func (s *stubProjectPlatformClient) SendEvent(int64, string) error                { return nil }
func (s *stubProjectPlatformClient) SendToChannel(string, string, string) error   { return nil }
func (s *stubProjectPlatformClient) WhoAmI() (*InstallIdentity, error)            { return s.identity, nil }
func (s *stubProjectPlatformClient) ExecuteIntegrationTool(int64, string, map[string]any) (*ExecuteResult, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) CallApp(string, string, map[string]any) (json.RawMessage, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) CallAppResult(string, string, map[string]any, any) error {
	return nil
}
func (s *stubProjectPlatformClient) StartOAuth(OAuthStartRequest) (*OAuthStartResult, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) DisconnectConnection(int64) error { return nil }
func (s *stubProjectPlatformClient) ListOwnedConnections() ([]PlatformConnection, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) GetGrants(int64) (*GrantsResponse, error) { return nil, nil }
func (s *stubProjectPlatformClient) GetConnectionCredentials(int64) (*ConnectionCredentials, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) ListProjects() ([]PlatformProject, error) {
	return s.projects, nil
}
func (s *stubProjectPlatformClient) SpawnRealtimeThread(RealtimeSpawnRequest) (*RealtimeSpawnResult, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) KillThread(string) error              { return nil }
func (s *stubProjectPlatformClient) PlatformInfo() (*PlatformInfo, error) { return nil, nil }

func TestProjectScopedClientWhoAmIUsesScopedProjectMetadata(t *testing.T) {
	base := &stubProjectPlatformClient{
		identity: &InstallIdentity{
			InstallID:          42,
			AppName:            "media",
			ProjectID:          "",
			ProjectName:        "",
			ProjectDescription: "",
		},
		projects: []PlatformProject{
			{ID: "proj-a", Name: "Alpha", Description: "Alpha context"},
			{ID: "proj-b", Name: "Beta", Description: "Beta context"},
		},
	}
	scoped := wrapPlatformWithProject(base, "proj-b")
	got, err := scoped.WhoAmI()
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if got.ProjectID != "proj-b" {
		t.Fatalf("ProjectID=%q, want proj-b", got.ProjectID)
	}
	if got.ProjectName != "Beta" || got.ProjectDescription != "Beta context" {
		t.Fatalf("project metadata = (%q, %q), want Beta/Beta context", got.ProjectName, got.ProjectDescription)
	}
	if base.identity.ProjectID != "" {
		t.Fatalf("base identity was mutated: %#v", base.identity)
	}
}
