package agentplugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseManifestUsesAgentPluginsFailureBoundary(t *testing.T) {
	manifest, issues, err := ParseManifest([]byte(`{
		"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name":"crm",
		"unknown":true,
		"extensions":"ignored"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "crm" || manifest.Extensions != nil {
		t.Fatalf("manifest=%+v", manifest)
	}
	if len(issues) != 2 {
		t.Fatalf("issues=%+v, want two non-fatal reports", issues)
	}
}

func TestParseManifestRejectsSchemaInvalidNulls(t *testing.T) {
	for _, field := range []string{"version", "description", "author", "homepage", "repository", "license", "keywords"} {
		document := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"crm","` + field + `":null}`
		if _, _, err := ParseManifest([]byte(document)); err == nil || !strings.Contains(err.Error(), "must not be null") {
			t.Fatalf("field %s: err=%v", field, err)
		}
	}
}

func TestLoadDirIsolatesInvalidSkillAndMCPEntry(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "plugin.json"), `{
		"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name":"crm",
		"extensions":{"com.apteva":{"manifest":"apteva.yaml"}}
	}`)
	writeTestFile(t, filepath.Join(root, "skills", "crm", "SKILL.md"), `---
name: crm
description: Work with CRM contacts and opportunities when the user asks about customers, leads, or pipeline.
metadata:
  author: apteva
---
Use the CRM tools.`)
	writeTestFile(t, filepath.Join(root, "skills", "broken", "SKILL.md"), `no frontmatter`)
	writeTestFile(t, filepath.Join(root, "mcp.json"), `{
		"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
		"mcpServers":{
			"remote":{"type":"streamable-http","url":"https://example.test/mcp"},
			"broken":{"type":"stdio","command":"../escape"}
		}
	}`)

	pkg, err := LoadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.Skills) != 1 || pkg.Skills[0].Name != "crm" {
		t.Fatalf("skills=%+v", pkg.Skills)
	}
	if pkg.MCP == nil || len(pkg.MCP.Servers) != 1 || pkg.MCP.Servers["remote"].Type != "streamable-http" {
		t.Fatalf("mcp=%+v", pkg.MCP)
	}
	if len(pkg.Issues) != 2 {
		t.Fatalf("issues=%+v, want invalid skill and invalid MCP entry", pkg.Issues)
	}
	ext, ok, err := pkg.Manifest.Apteva()
	if err != nil || !ok || ext.Manifest != "apteva.yaml" {
		t.Fatalf("apteva extension=%+v ok=%v err=%v", ext, ok, err)
	}
}

func TestLoadDirRejectsEscapingSkillSymlink(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "plugin.json"), `{
		"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name":"safe-plugin"
	}`)
	outside := filepath.Join(t.TempDir(), "SKILL.md")
	writeTestFile(t, outside, `---
name: escaped
description: This should never be loaded because it is outside the package boundary.
---
No.`)
	dir := filepath.Join(root, "skills", "escaped")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	pkg, err := LoadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.Skills) != 0 || len(pkg.Issues) != 1 || !strings.Contains(pkg.Issues[0].Message, "outside") {
		t.Fatalf("package=%+v", pkg)
	}
}

func TestLoadDirAllowsContainedSkillsSymlink(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "plugin.json"), `{
		"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name":"safe-plugin"
	}`)
	writeTestFile(t, filepath.Join(root, "components", "crm", "SKILL.md"), `---
name: crm
description: Use CRM tools when the user asks about contacts, leads, opportunities, or pipeline.
---
Use CRM.`)
	if err := os.Symlink(filepath.Join(root, "components"), filepath.Join(root, "skills")); err != nil {
		t.Fatal(err)
	}
	pkg, err := LoadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.Issues) != 0 || len(pkg.Skills) != 1 || pkg.Skills[0].Name != "crm" {
		t.Fatalf("package=%+v", pkg)
	}
}

func TestMCPRejectsSchemaInvalidOptionalNulls(t *testing.T) {
	for _, entry := range []string{
		`{"type":"stdio","command":"node","args":null}`,
		`{"type":"stdio","command":"node","env":null}`,
		`{"type":"stdio","command":"node","cwd":null}`,
		`{"type":"streamable-http","url":"https://example.test/mcp","headers":null}`,
	} {
		if _, err := parseMCPServer(json.RawMessage(entry)); err == nil {
			t.Fatalf("parseMCPServer(%s) unexpectedly succeeded", entry)
		}
	}
}

func TestPrepareStdioExpandsVariablesAndEnforcesContainment(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin", "server")
	writeTestFile(t, bin, "#!/bin/sh\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "data")
	runtime, err := PrepareStdio(root, data, MCPServer{
		Type: "stdio", Command: "./bin/server", Cwd: "./work",
		Args: []string{"--data", "${PLUGIN_DATA}/state"},
		Env:  map[string]string{"CONFIG": "${PLUGIN_ROOT}/config.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolvedRoot, _ := filepath.EvalSymlinks(root)
	resolvedData, _ := filepath.EvalSymlinks(data)
	if runtime.Command != filepath.Join(resolvedRoot, "bin", "server") || runtime.Cwd != filepath.Join(resolvedRoot, "work") {
		t.Fatalf("runtime=%+v", runtime)
	}
	if runtime.Args[1] != filepath.Join(resolvedData, "state") || runtime.Env["PLUGIN_ROOT"] != resolvedRoot || runtime.Env["PLUGIN_DATA"] != resolvedData {
		t.Fatalf("runtime expansion=%+v", runtime)
	}
}

func TestRemoteURLRequiresHTTPSOffLoopback(t *testing.T) {
	for _, raw := range []string{"http://example.com/mcp", "https://user@example.com/mcp", "https://example.com/mcp#fragment"} {
		if err := validateRemoteURL(raw); err == nil {
			t.Fatalf("validateRemoteURL(%q) unexpectedly succeeded", raw)
		}
	}
	for _, raw := range []string{"https://example.com/mcp", "http://127.0.0.1:9000/mcp", "http://localhost/mcp"} {
		if err := validateRemoteURL(raw); err != nil {
			t.Fatalf("validateRemoteURL(%q): %v", raw, err)
		}
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
