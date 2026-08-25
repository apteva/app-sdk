package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectTemplatesHTTPContractAndTypedDecode(t *testing.T) {
	t.Setenv("APTEVA_INSTALL_ID", "42")
	definition := `{"category":"business","agents":[{"key":"advisor","name":"Advisor","directive":"Help the client","mode":"autonomous","apps":["crm"]}],"dashboard":["crm:overview"],"dashboard_layout":[{"id":"crm-1","component":"crm:overview","size":"wide","settings":{"pipeline":"active"}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer app-token" {
			t.Errorf("Authorization=%q", got)
		}
		if got := r.Header.Get("X-Apteva-App-Install-ID"); got != "42" {
			t.Errorf("install header=%q", got)
		}
		if got := r.URL.Query().Get("project_id"); got != "project-a" {
			t.Errorf("project_id=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/apps/callback/templates":
			if got := r.URL.Query().Get("include_system"); got != "true" {
				t.Errorf("include_system=%q", got)
			}
			_, _ = w.Write([]byte(`{"templates":[{"id":"consulting","kind":"project_setup","name":"Consulting","source":"user","owner_project_id":"project-a","schema_version":2,"revision":1,"definition":` + definition + `}]}`))
		case "/api/apps/callback/templates/consulting":
			_, _ = w.Write([]byte(`{"id":"consulting","kind":"project_setup","name":"Consulting","source":"user","owner_project_id":"project-a","schema_version":2,"revision":1,"definition":` + definition + `}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := (&AppCtx{platform: newHTTPPlatformClient(server.URL, "app-token")}).WithProject("project-a")
	api := ctx.ProjectTemplatesAPI()
	if api == nil {
		t.Fatal("default AppCtx did not expose ProjectTemplatesAPI")
	}
	templates, err := api.ListProjectTemplates("", ProjectTemplateListOptions{IncludeSystem: true})
	if err != nil {
		t.Fatalf("ListProjectTemplates: %v", err)
	}
	if len(templates) != 1 || templates[0].ID != "consulting" {
		t.Fatalf("templates=%+v", templates)
	}
	typed, err := templates[0].DecodeProjectSetup()
	if err != nil {
		t.Fatalf("DecodeProjectSetup: %v", err)
	}
	if typed.Category != "business" || len(typed.Agents) != 1 || typed.Agents[0].Apps[0] != "crm" {
		t.Fatalf("definition=%+v", typed)
	}
	if len(typed.DashboardLayout) != 1 || typed.DashboardLayout[0].Settings["pipeline"] != "active" {
		t.Fatalf("dashboard_layout=%+v", typed.DashboardLayout)
	}

	template, err := api.GetProjectTemplate("", "consulting")
	if err != nil {
		t.Fatalf("GetProjectTemplate: %v", err)
	}
	if template.ID != "consulting" || template.OwnerProjectID != "project-a" {
		t.Fatalf("template=%+v", template)
	}
}

func TestProjectTemplatesProjectScopeAndOptionalCapability(t *testing.T) {
	plain := (&AppCtx{platform: &stubProjectPlatformClient{}}).WithProject("project-a")
	if plain.ProjectTemplatesAPI() != nil {
		t.Fatal("plain PlatformClient unexpectedly implements ProjectTemplateClient")
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"templates":[]}`))
	}))
	defer server.Close()
	api := (&AppCtx{platform: newHTTPPlatformClient(server.URL, "token")}).WithProject("project-a").ProjectTemplatesAPI()
	if _, err := api.ListProjectTemplates("project-b", ProjectTemplateListOptions{}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("sibling project error=%v", err)
	}
	if _, err := api.GetProjectTemplate("project-b", "template"); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("sibling get error=%v", err)
	}
	if requests != 0 {
		t.Fatalf("out-of-scope calls reached server: %d", requests)
	}
}

func TestProjectTemplateValidationAndKind(t *testing.T) {
	client := newHTTPPlatformClient("http://127.0.0.1:1", "token").(ProjectTemplateClient)
	if _, err := client.ListProjectTemplates("", ProjectTemplateListOptions{}); err == nil {
		t.Fatal("empty project id accepted")
	}
	for _, id := range []string{"", "a/b", `a\b`} {
		if _, err := client.GetProjectTemplate("project-a", id); err == nil {
			t.Fatalf("template id %q accepted", id)
		}
	}
	wrong := ProjectTemplate{Kind: "future_kind", Definition: json.RawMessage(`{}`)}
	if _, err := wrong.DecodeProjectSetup(); err == nil || !strings.Contains(err.Error(), "not a project_setup") {
		t.Fatalf("wrong-kind error=%v", err)
	}
}

func TestProjectTemplatePermissionIsKnownAndDescribed(t *testing.T) {
	if !containsPermission(AllPermissions(), PermTemplatesRead) {
		t.Fatalf("missing permission %q", PermTemplatesRead)
	}
	if PermissionDescription(PermTemplatesRead) == string(PermTemplatesRead) {
		t.Fatalf("permission %q lacks install-consent description", PermTemplatesRead)
	}
}
