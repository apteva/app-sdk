// Package agentplugin implements the portable discovery and validation layer
// from Agent Plugins 1.0.0. It deliberately does not define installation,
// permissions, credentials, UI, or sandbox policy; those remain the host
// client's responsibility.
package agentplugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ManifestSchema  = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	MCPSchema       = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
	AptevaNamespace = "com.apteva"
	maxDocumentSize = 256 * 1024
)

var (
	pluginNamePattern = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?)$`)
	skillNamePattern  = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)$`)
)

type Author struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

// Manifest is the portable plugin.json contract. Extension values remain raw
// because their schemas are owned by individual clients.
type Manifest struct {
	Schema      string                     `json:"$schema"`
	Name        string                     `json:"name"`
	Version     string                     `json:"version,omitempty"`
	Description string                     `json:"description,omitempty"`
	Author      *Author                    `json:"author,omitempty"`
	Homepage    string                     `json:"homepage,omitempty"`
	Repository  string                     `json:"repository,omitempty"`
	License     string                     `json:"license,omitempty"`
	Keywords    []string                   `json:"keywords,omitempty"`
	Extensions  map[string]json.RawMessage `json:"extensions,omitempty"`
}

type AptevaExtension struct {
	// Manifest is a plugin-root-relative path to the native Apteva manifest.
	// It is client-owned metadata; other Agent Plugins clients ignore it.
	Manifest string `json:"manifest"`
}

func (m Manifest) Apteva() (*AptevaExtension, bool, error) {
	raw, ok := m.Extensions[AptevaNamespace]
	if !ok {
		return nil, false, nil
	}
	var ext AptevaExtension
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ext); err != nil {
		return nil, true, fmt.Errorf("%s extension: %w", AptevaNamespace, err)
	}
	if strings.TrimSpace(ext.Manifest) == "" {
		return nil, true, fmt.Errorf("%s extension: manifest required", AptevaNamespace)
	}
	if err := validatePortableRelativePath(ext.Manifest); err != nil {
		return nil, true, fmt.Errorf("%s extension manifest: %w", AptevaNamespace, err)
	}
	return &ext, true, nil
}

type Issue struct {
	Component string `json:"component"`
	Name      string `json:"name,omitempty"`
	Message   string `json:"message"`
}

type Skill struct {
	Name          string
	Description   string
	License       string
	Compatibility string
	Metadata      map[string]string
	AllowedTools  string
	Body          string
	Dir           string
	Path          string
}

type MCPConfig struct {
	Schema  string
	Servers map[string]MCPServer
}

type MCPServer struct {
	Type    string
	Command string
	Args    []string
	Env     map[string]string
	Cwd     string
	URL     string
	Headers map[string]string
}

type Package struct {
	Root     string
	Manifest Manifest
	Skills   []Skill
	MCP      *MCPConfig
	Issues   []Issue
}

// ParseManifest applies the Agent Plugins 1.0.0 manifest failure boundary:
// unknown top-level fields and a non-object extensions value are reported and
// ignored; every other schema violation is fatal.
func ParseManifest(data []byte) (Manifest, []Issue, error) {
	var raw map[string]json.RawMessage
	if err := decodeSingleJSON(data, &raw); err != nil {
		return Manifest{}, nil, fmt.Errorf("parse plugin.json: %w", err)
	}
	issues := []Issue{}
	allowed := map[string]bool{
		"$schema": true, "name": true, "version": true, "description": true,
		"author": true, "homepage": true, "repository": true, "license": true,
		"keywords": true, "extensions": true,
	}
	for key := range raw {
		if !allowed[key] {
			issues = append(issues, Issue{Component: "manifest", Name: key, Message: "unknown top-level field ignored"})
			delete(raw, key)
		}
	}
	// encoding/json accepts JSON null for strings, slices, and pointers. The
	// Agent Plugins schema does not, so reject null for every known manifest
	// field except extensions, whose non-object failure boundary is explicitly
	// non-fatal below.
	for _, key := range []string{"version", "description", "author", "homepage", "repository", "license", "keywords"} {
		if value, ok := raw[key]; ok && isJSONNull(value) {
			return Manifest{}, issues, fmt.Errorf("%s must not be null", key)
		}
	}
	if ext, ok := raw["extensions"]; ok {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(ext, &object); err != nil || object == nil {
			issues = append(issues, Issue{Component: "manifest", Name: "extensions", Message: "non-object extensions field ignored"})
			delete(raw, "extensions")
		} else {
			for namespace, value := range object {
				var extensionObject map[string]json.RawMessage
				if err := json.Unmarshal(value, &extensionObject); err != nil || extensionObject == nil {
					return Manifest{}, issues, fmt.Errorf("extensions.%s must be an object", namespace)
				}
			}
		}
	}
	clean, _ := json.Marshal(raw)
	var manifest Manifest
	dec := json.NewDecoder(strings.NewReader(string(clean)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return Manifest{}, issues, fmt.Errorf("validate plugin.json: %w", err)
	}
	if manifest.Schema != ManifestSchema {
		return Manifest{}, issues, fmt.Errorf("unsupported $schema %q", manifest.Schema)
	}
	if len(manifest.Name) < 1 || len(manifest.Name) > 64 ||
		!pluginNamePattern.MatchString(manifest.Name) ||
		strings.Contains(manifest.Name, "--") || strings.Contains(manifest.Name, "..") {
		return Manifest{}, issues, fmt.Errorf("name %q does not satisfy Agent Plugins 1.0.0", manifest.Name)
	}
	return manifest, issues, nil
}

// LoadDir loads a filesystem package with the component-level isolation rules
// from Agent Plugins 1.0.0. A bad manifest rejects the package. A bad skill,
// skills directory, mcp.json document, or individual MCP server is reported at
// its narrowest boundary while valid sibling components continue loading.
func LoadDir(root string) (*Package, error) {
	resolvedRoot, err := resolveRoot(root)
	if err != nil {
		return nil, err
	}
	manifestPath, err := containedPath(resolvedRoot, filepath.Join(resolvedRoot, "plugin.json"), true)
	if err != nil {
		return nil, fmt.Errorf("plugin.json: %w", err)
	}
	manifestData, err := readLimitedFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("plugin.json: %w", err)
	}
	manifest, issues, err := ParseManifest(manifestData)
	if err != nil {
		return nil, err
	}
	pkg := &Package{Root: resolvedRoot, Manifest: manifest, Issues: issues}
	pkg.loadSkills()
	pkg.loadMCP()
	return pkg, nil
}

func (p *Package) loadSkills() {
	skillsPath := filepath.Join(p.Root, "skills")
	_, err := os.Lstat(skillsPath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		p.Issues = append(p.Issues, Issue{Component: "skills", Message: err.Error()})
		return
	}
	resolved, err := containedPath(p.Root, skillsPath, false)
	if err != nil {
		message := "fixed skills path is not a directory"
		message = err.Error()
		p.Issues = append(p.Issues, Issue{Component: "skills", Message: message})
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		p.Issues = append(p.Issues, Issue{Component: "skills", Message: "fixed skills path is not a directory"})
		return
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		p.Issues = append(p.Issues, Issue{Component: "skills", Message: err.Error()})
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		skillDir, err := containedPath(p.Root, filepath.Join(resolved, name), false)
		if err != nil {
			p.Issues = append(p.Issues, Issue{Component: "skill", Name: name, Message: err.Error()})
			continue
		}
		info, err := os.Stat(skillDir)
		if err != nil || !info.IsDir() {
			continue
		}
		skillPath := filepath.Join(skillDir, "SKILL.md")
		info, err = os.Stat(skillPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		contained, err := containedPath(p.Root, skillPath, true)
		if err != nil {
			p.Issues = append(p.Issues, Issue{Component: "skill", Name: name, Message: err.Error()})
			continue
		}
		data, err := readLimitedFile(contained)
		if err != nil {
			p.Issues = append(p.Issues, Issue{Component: "skill", Name: name, Message: err.Error()})
			continue
		}
		skill, err := parseSkill(name, contained, data)
		if err != nil {
			p.Issues = append(p.Issues, Issue{Component: "skill", Name: name, Message: err.Error()})
			continue
		}
		p.Skills = append(p.Skills, skill)
	}
}

func (p *Package) loadMCP() {
	mcpPath := filepath.Join(p.Root, "mcp.json")
	_, err := os.Lstat(mcpPath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		p.Issues = append(p.Issues, Issue{Component: "mcp", Message: err.Error()})
		return
	}
	contained, err := containedPath(p.Root, mcpPath, true)
	if err != nil {
		p.Issues = append(p.Issues, Issue{Component: "mcp", Message: err.Error()})
		return
	}
	data, err := readLimitedFile(contained)
	if err != nil {
		p.Issues = append(p.Issues, Issue{Component: "mcp", Message: err.Error()})
		return
	}
	config, issues, err := parseMCP(data)
	if err != nil {
		p.Issues = append(p.Issues, Issue{Component: "mcp", Message: err.Error()})
		return
	}
	p.MCP = config
	p.Issues = append(p.Issues, issues...)
}

type skillFrontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  string            `yaml:"allowed-tools"`
}

func parseSkill(dirName, path string, data []byte) (Skill, error) {
	text := strings.TrimLeft(string(data), "\ufeff\r\n\t ")
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return Skill{}, errors.New("SKILL.md requires YAML frontmatter")
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(text, "---\r\n"), "---\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return Skill{}, errors.New("SKILL.md frontmatter has no closing ---")
	}
	frontmatter := rest[:end]
	body := strings.TrimLeft(rest[end+len("\n---"):], "\r\n")
	var front skillFrontmatter
	dec := yaml.NewDecoder(strings.NewReader(frontmatter))
	dec.KnownFields(true)
	if err := dec.Decode(&front); err != nil {
		return Skill{}, fmt.Errorf("invalid frontmatter: %w", err)
	}
	if len(front.Name) < 1 || len(front.Name) > 64 || !skillNamePattern.MatchString(front.Name) || strings.Contains(front.Name, "--") {
		return Skill{}, fmt.Errorf("invalid skill name %q", front.Name)
	}
	if front.Name != dirName {
		return Skill{}, fmt.Errorf("skill name %q must match directory %q", front.Name, dirName)
	}
	if len(front.Description) < 1 || len(front.Description) > 1024 {
		return Skill{}, errors.New("description must contain 1-1024 characters")
	}
	if front.Compatibility != "" && len(front.Compatibility) > 500 {
		return Skill{}, errors.New("compatibility exceeds 500 characters")
	}
	return Skill{
		Name: front.Name, Description: front.Description, License: front.License,
		Compatibility: front.Compatibility, Metadata: front.Metadata,
		AllowedTools: front.AllowedTools, Body: body,
		Dir: filepath.Dir(path), Path: path,
	}, nil
}

func parseMCP(data []byte) (*MCPConfig, []Issue, error) {
	var top map[string]json.RawMessage
	if err := decodeSingleJSON(data, &top); err != nil {
		return nil, nil, fmt.Errorf("parse mcp.json: %w", err)
	}
	for key := range top {
		if key != "$schema" && key != "mcpServers" {
			return nil, nil, fmt.Errorf("unknown top-level field %q", key)
		}
	}
	var schema string
	if err := json.Unmarshal(top["$schema"], &schema); err != nil || schema != MCPSchema {
		return nil, nil, fmt.Errorf("unsupported $schema %q", schema)
	}
	var rawServers map[string]json.RawMessage
	if err := json.Unmarshal(top["mcpServers"], &rawServers); err != nil || rawServers == nil {
		return nil, nil, errors.New("mcpServers must be an object")
	}
	config := &MCPConfig{Schema: schema, Servers: map[string]MCPServer{}}
	issues := []Issue{}
	names := make([]string, 0, len(rawServers))
	for name := range rawServers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		server, err := parseMCPServer(rawServers[name])
		if err != nil {
			issues = append(issues, Issue{Component: "mcp_server", Name: name, Message: err.Error()})
			continue
		}
		config.Servers[name] = server
	}
	return config, issues, nil
}

func parseMCPServer(data json.RawMessage) (MCPServer, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return MCPServer{}, errors.New("server entry must be an object")
	}
	var typ string
	if err := json.Unmarshal(raw["type"], &typ); err != nil {
		return MCPServer{}, errors.New("type required")
	}
	allowed := map[string]bool{"type": true}
	server := MCPServer{Type: typ}
	switch typ {
	case "stdio":
		allowed["command"], allowed["args"], allowed["env"], allowed["cwd"] = true, true, true, true
		if err := json.Unmarshal(raw["command"], &server.Command); err != nil || server.Command == "" {
			return MCPServer{}, errors.New("command required")
		}
		if filepath.IsAbs(server.Command) || (strings.ContainsAny(server.Command, `/\\`) && !strings.HasPrefix(server.Command, "./")) {
			return MCPServer{}, errors.New("command must be a bare executable name or begin with ./")
		}
		if value, ok := raw["args"]; ok {
			if isJSONNull(value) || json.Unmarshal(value, &server.Args) != nil {
				return MCPServer{}, errors.New("args must be an array of strings")
			}
		}
		if value, ok := raw["env"]; ok {
			if isJSONNull(value) || json.Unmarshal(value, &server.Env) != nil {
				return MCPServer{}, errors.New("env must be an object of strings")
			}
		}
		if _, exists := server.Env["PLUGIN_ROOT"]; exists {
			return MCPServer{}, errors.New("env cannot override PLUGIN_ROOT")
		}
		if _, exists := server.Env["PLUGIN_DATA"]; exists {
			return MCPServer{}, errors.New("env cannot override PLUGIN_DATA")
		}
		if value, ok := raw["cwd"]; ok {
			if isJSONNull(value) || json.Unmarshal(value, &server.Cwd) != nil {
				return MCPServer{}, errors.New("cwd must be a string")
			}
			if !strings.HasPrefix(server.Cwd, "./") && server.Cwd != "${PLUGIN_ROOT}" &&
				!strings.HasPrefix(server.Cwd, "${PLUGIN_ROOT}/") && server.Cwd != "${PLUGIN_DATA}" &&
				!strings.HasPrefix(server.Cwd, "${PLUGIN_DATA}/") {
				return MCPServer{}, errors.New("cwd must be plugin-relative, PLUGIN_ROOT-rooted, or PLUGIN_DATA-rooted")
			}
		}
	case "streamable-http", "sse":
		allowed["url"], allowed["headers"] = true, true
		if err := json.Unmarshal(raw["url"], &server.URL); err != nil || server.URL == "" {
			return MCPServer{}, errors.New("url required")
		}
		if err := validateRemoteURL(server.URL); err != nil {
			return MCPServer{}, err
		}
		if value, ok := raw["headers"]; ok {
			if isJSONNull(value) || json.Unmarshal(value, &server.Headers) != nil {
				return MCPServer{}, errors.New("headers must be an object of strings")
			}
		}
	default:
		return MCPServer{}, fmt.Errorf("unsupported transport %q", typ)
	}
	for key := range raw {
		if !allowed[key] {
			return MCPServer{}, fmt.Errorf("unknown field %q for transport %s", key, typ)
		}
	}
	return server, nil
}

type StdioRuntime struct {
	Command string
	Args    []string
	Env     map[string]string
	Cwd     string
}

// PrepareStdio resolves a validated stdio entry without invoking a shell.
// The host may sanitize its ambient environment independently; this map is
// only the portable plugin overlay, with client-owned variables applied last.
func PrepareStdio(root, dataDir string, server MCPServer) (StdioRuntime, error) {
	if server.Type != "stdio" {
		return StdioRuntime{}, fmt.Errorf("server type %q is not stdio", server.Type)
	}
	resolvedRoot, err := resolveRoot(root)
	if err != nil {
		return StdioRuntime{}, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return StdioRuntime{}, fmt.Errorf("create PLUGIN_DATA: %w", err)
	}
	resolvedData, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return StdioRuntime{}, err
	}
	resolvedData, err = filepath.Abs(resolvedData)
	if err != nil {
		return StdioRuntime{}, err
	}
	command := server.Command
	if strings.HasPrefix(command, "./") {
		command, err = containedPath(resolvedRoot, filepath.Join(resolvedRoot, filepath.FromSlash(strings.TrimPrefix(command, "./"))), true)
		if err != nil {
			return StdioRuntime{}, fmt.Errorf("command: %w", err)
		}
	}
	// One replacer performs the specification's single expansion pass. This
	// prevents a placeholder present inside a replacement value from being
	// interpreted a second time.
	replacer := strings.NewReplacer("${PLUGIN_ROOT}", resolvedRoot, "${PLUGIN_DATA}", resolvedData)
	expand := replacer.Replace
	args := make([]string, len(server.Args))
	for i, arg := range server.Args {
		args[i] = expand(arg)
	}
	env := make(map[string]string, len(server.Env)+2)
	for key, value := range server.Env {
		env[key] = expand(value)
	}
	env["PLUGIN_ROOT"] = resolvedRoot
	env["PLUGIN_DATA"] = resolvedData
	cwd := resolvedRoot
	if server.Cwd != "" {
		cwd = expand(server.Cwd)
		if strings.HasPrefix(server.Cwd, "./") {
			cwd = filepath.Join(resolvedRoot, filepath.FromSlash(strings.TrimPrefix(server.Cwd, "./")))
		}
		base := resolvedRoot
		if server.Cwd == "${PLUGIN_DATA}" || strings.HasPrefix(server.Cwd, "${PLUGIN_DATA}/") {
			base = resolvedData
		}
		cwd, err = containedPath(base, cwd, false)
		if err != nil {
			return StdioRuntime{}, fmt.Errorf("cwd: %w", err)
		}
	}
	return StdioRuntime{Command: command, Args: args, Env: env, Cwd: cwd}, nil
}

func validateRemoteURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("url must be absolute HTTP or HTTPS")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("url must use HTTP or HTTPS")
	}
	if u.User != nil || u.Fragment != "" {
		return errors.New("url cannot contain user information or a fragment")
	}
	host := strings.Trim(strings.ToLower(u.Hostname()), "[]")
	loopback := host == "localhost"
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		loopback = true
	}
	if !loopback && u.Scheme != "https" {
		return errors.New("non-loopback MCP endpoints must use HTTPS")
	}
	return nil
}

func validatePortableRelativePath(value string) error {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") {
		return errors.New("path must be relative to the plugin root")
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("path escapes the plugin root")
	}
	return nil
}

func resolveRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve plugin root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("plugin root is not a directory")
	}
	return resolved, nil
}

func containedPath(root, path string, requireRegular bool) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path resolves outside the plugin root")
	}
	if requireRegular {
		info, err := os.Stat(resolved)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("path is not a regular file")
		}
	}
	return resolved, nil
}

func readLimitedFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxDocumentSize {
		return nil, fmt.Errorf("document exceeds %d bytes", maxDocumentSize)
	}
	return os.ReadFile(path)
}

func decodeSingleJSON(data []byte, value any) error {
	if len(data) > maxDocumentSize {
		return fmt.Errorf("document exceeds %d bytes", maxDocumentSize)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func isJSONNull(value json.RawMessage) bool {
	return string(bytes.TrimSpace(value)) == "null"
}
