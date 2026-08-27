package sdk

import (
	"encoding/json"
	"testing"
)

type stubProjectPlatformClient struct {
	identity *InstallIdentity
	projects []PlatformProject
}

type stubThreadProjectPlatformClient struct {
	*stubProjectPlatformClient
	spawn  ThreadSpawnRequest
	ensure ThreadEnsureRequest
}

func (s *stubThreadProjectPlatformClient) SendThreadEvent(ThreadRef, any) error { return nil }
func (s *stubThreadProjectPlatformClient) SpawnThread(req ThreadSpawnRequest) (*ThreadSpawnResult, error) {
	s.spawn = req
	return &ThreadSpawnResult{Thread: ThreadRef{AgentID: req.AgentID, ThreadID: req.ThreadID}}, nil
}
func (s *stubThreadProjectPlatformClient) EnsureThread(req ThreadEnsureRequest) (*ThreadEnsureResult, error) {
	s.ensure = req
	return &ThreadEnsureResult{Thread: ThreadRef{AgentID: req.AgentID, ThreadID: req.ThreadID}}, nil
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
func (s *stubProjectPlatformClient) GetIntegrationURLProperty(int64, string) (*IntegrationURLPropertyStatus, error) {
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
func (s *stubProjectPlatformClient) EnsureIntegrationWebhook(IntegrationWebhookEnsureRequest) (*IntegrationWebhookStatus, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) VerifyIntegrationWebhook(IntegrationWebhookVerifyRequest) (*IntegrationWebhookVerifyResult, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) GetConnectionCredentials(int64) (*ConnectionCredentials, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) ListProjects() ([]PlatformProject, error) {
	return s.projects, nil
}
func (s *stubProjectPlatformClient) ExposeIngress(IngressExposeRequest) (*IngressRoute, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) UnexposeIngress(string) error { return nil }
func (s *stubProjectPlatformClient) ListIngressRoutes() ([]IngressRoute, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) ListDomainGrants() ([]DomainGrant, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) UpsertDNSRecord(DNSRecordRequest) (*DNSRecordResult, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) DeleteDNSRecord(DNSRecordRequest) (*DNSRecordResult, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) SpawnRealtimeThread(RealtimeSpawnRequest) (*RealtimeSpawnResult, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) RenewRealtimeAudioBridge(int64, string) (*RealtimeSpawnResult, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) KillThread(int64, string) error       { return nil }
func (s *stubProjectPlatformClient) PlatformInfo() (*PlatformInfo, error) { return nil, nil }
func (s *stubProjectPlatformClient) ListEnvironments() ([]EnvironmentSummary, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) CreateEnvironment(EnvironmentCreateRequest) (*EnvironmentSummary, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) GetEnvironment(string) (*EnvironmentSummary, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) DestroyEnvironment(string) error { return nil }
func (s *stubProjectPlatformClient) SeedEnvironment(string, []EnvironmentSeedCall, string) ([]json.RawMessage, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) CallEnvironmentApp(string, string, string, map[string]any) (json.RawMessage, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) CallEnvironmentAppResult(string, string, string, map[string]any, any) error {
	return nil
}
func (s *stubProjectPlatformClient) SnapshotEnvironment(string, EnvironmentSnapshotRequest) (*EnvironmentSnapshot, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) ListEnvironmentAgents(string) ([]EnvironmentAgent, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) SpawnEnvironmentAgent(string, EnvironmentAgentSpawnRequest) (*EnvironmentAgent, error) {
	return nil, nil
}
func (s *stubProjectPlatformClient) StopEnvironmentAgent(string, string) error { return nil }

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

func TestProjectScopedClientDefaultsOrdinaryThreadProject(t *testing.T) {
	base := &stubThreadProjectPlatformClient{stubProjectPlatformClient: &stubProjectPlatformClient{}}
	scoped := wrapPlatformWithProject(base, "proj-thread")

	threads, ok := scoped.(ThreadClient)
	if !ok {
		t.Fatal("project-scoped client must preserve ThreadClient")
	}
	if _, err := threads.SpawnThread(ThreadSpawnRequest{AgentID: 7, ThreadID: "chat-1"}); err != nil {
		t.Fatal(err)
	}
	if base.spawn.ProjectID != "proj-thread" {
		t.Fatalf("spawn project_id=%q, want proj-thread", base.spawn.ProjectID)
	}

	profiles, ok := scoped.(ThreadProfileClient)
	if !ok {
		t.Fatal("project-scoped client must preserve ThreadProfileClient")
	}
	if _, err := profiles.EnsureThread(ThreadEnsureRequest{ThreadSpawnRequest: ThreadSpawnRequest{
		AgentID: 7, ThreadID: "chat-1", ProjectID: "explicit-project",
	}}); err != nil {
		t.Fatal(err)
	}
	if base.ensure.ProjectID != "explicit-project" {
		t.Fatalf("ensure project_id=%q, want explicit-project", base.ensure.ProjectID)
	}
}
