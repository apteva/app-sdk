package sdk

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
)

// ComponentDeclarations is optional release metadata. Declaring a component
// does not provision infrastructure or grant deployment administration rights.
type ComponentDeclarations map[string]ComponentDeclaration

type ComponentDeclaration struct {
	Protocol string `yaml:"protocol" json:"protocol"`
	// Required is setup/readiness guidance; it does not block parent startup.
	Required     bool                   `yaml:"required,omitempty" json:"required,omitempty"`
	Capabilities RuntimeAppCapabilities `yaml:"capabilities" json:"capabilities"`
	Manifest     Manifest               `yaml:"manifest" json:"manifest"`
}

var componentNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var componentProtocolPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]*/v[1-9][0-9]*$`)

func ValidateComponentName(name string) error {
	if !componentNamePattern.MatchString(name) {
		return fmt.Errorf("invalid component name %q", name)
	}
	return nil
}

func ValidateComponentProtocol(protocol string) error {
	if len(protocol) > 128 || !componentProtocolPattern.MatchString(protocol) {
		return errors.New("component requires a versioned protocol such as example.executor/v1")
	}
	return nil
}

// ValidateComponentDeclarations normalizes nested worker manifests using the
// ordinary manifest validator. Nested components are rejected before recursion,
// including cyclic programmatically constructed manifests.
func ValidateComponentDeclarations(components ComponentDeclarations) error {
	if len(components) > 16 {
		return errors.New("at most 16 app components are supported")
	}
	names := make([]string, 0, len(components))
	for name := range components {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ValidateComponentName(name); err != nil {
			return err
		}
		def := components[name]
		if err := ValidateComponentProtocol(def.Protocol); err != nil {
			return fmt.Errorf("component %s: %w", name, err)
		}
		caps := def.Capabilities
		if !caps.PassiveStart || !caps.Drain || (caps.Routing != "stateless" && caps.Routing != "key") {
			return fmt.Errorf("component %s requires passive_start, drain and an explicit routing mode", name)
		}
		worker := &def.Manifest
		if len(worker.Components) != 0 {
			return fmt.Errorf("component %s: nested owned components are not supported", name)
		}
		if err := ValidateManifest(worker); err != nil {
			return fmt.Errorf("component %s manifest: %w", name, err)
		}
		p := worker.Provides
		if worker.Runtime.Kind == "static" || len(p.UIPanels) != 0 || p.UIApp != nil || len(p.UIComponents) != 0 || len(p.UISurfaces) != 0 || len(p.Channels) != 0 || len(p.PromptFragments) != 0 || len(p.Skills) != 0 {
			return fmt.Errorf("component %s must be a private service", name)
		}
		if len(worker.Requires.Apps) != 0 || len(worker.Requires.Integrations) != 0 {
			return fmt.Errorf("component %s must receive scoped job inputs rather than inherit app or integration dependencies", name)
		}
		for _, route := range p.HTTPRoutes {
			if route.NoAuth {
				return fmt.Errorf("component %s cannot expose public routes", name)
			}
		}
		if source := worker.Runtime.Source; source != nil {
			ref, err := hex.DecodeString(source.Ref)
			if err != nil || (len(ref) != 20 && len(ref) != 32) {
				return fmt.Errorf("component %s source must use an immutable commit", name)
			}
		} else if len(worker.Runtime.Artifacts) == 0 {
			return fmt.Errorf("component %s requires pinned source or verified artifacts", name)
		}
		for _, artifact := range worker.Runtime.Artifacts {
			digest, err := hex.DecodeString(artifact.SHA256)
			if err != nil || len(digest) != 32 {
				return fmt.Errorf("component %s requires verified artifacts", name)
			}
		}
		if pkg := worker.Runtime.Package; pkg != nil && pkg.AppCapabilities != nil && *pkg.AppCapabilities != caps {
			return fmt.Errorf("component %s package capabilities conflict with its declaration", name)
		}
		components[name] = def
	}
	return nil
}

// ComponentClient calls this installation's declared private components. It
// reuses PlatformClient and AppContextClient; existing clients/stubs need no new
// methods. Server resolves ownership, placement and the active generation.
type ComponentClient struct{ app *AppCtx }

func (c *AppCtx) Components() *ComponentClient { return &ComponentClient{app: c} }

func (c *ComponentClient) target(name string) (PlatformClient, string, error) {
	if err := ValidateComponentName(name); err != nil {
		return nil, "", err
	}
	if c == nil || c.app == nil || c.app.platform == nil || c.app.manifest == nil {
		return nil, "", errors.New("component API unavailable")
	}
	if _, ok := c.app.manifest.Components[name]; !ok {
		return nil, "", fmt.Errorf("component %s is not declared by this app", name)
	}
	return c.app.platform, "component:" + name, nil
}

// Call decodes the existing inner tool result; cancellation, project scope and
// deadlines pass through the normal app-call transport without local fallback.
func (c *ComponentClient) Call(ctx context.Context, name, tool string, input map[string]any, out any) error {
	client, target, err := c.target(name)
	if err != nil {
		return err
	}
	return CallAppResultContext(ctx, client, target, tool, input, out)
}

// CallRaw preserves the normal app-call MCP envelope.
func (c *ComponentClient) CallRaw(ctx context.Context, name, tool string, input map[string]any) (json.RawMessage, error) {
	client, target, err := c.target(name)
	if err != nil {
		return nil, err
	}
	return CallAppContext(ctx, client, target, tool, input)
}

func (c *ComponentClient) CallBatch(ctx context.Context, name string, calls []AppCall, options AppBatchOptions) ([]AppCallResult, error) {
	client, target, err := c.target(name)
	if err != nil {
		return nil, err
	}
	return CallAppBatchContext(ctx, client, target, calls, options)
}

const ComponentDescribePath = "/_apteva/runtime/component/describe"

type ComponentDescriptor struct {
	Protocol string `json:"protocol"`
}

// ComponentService optionally advertises a worker's wire protocol. Run mounts
// its protected descriptor automatically. Managed lifecycle routes (passive
// start, activate and drain) remain the worker's responsibility.
type ComponentService interface {
	ComponentProtocol() string
}

func mountComponentRoutes(mux *http.ServeMux, app App) {
	service, ok := app.(ComponentService)
	if !ok {
		return
	}
	protocol := service.ComponentProtocol()
	mux.HandleFunc(ComponentDescribePath, func(w http.ResponseWriter, r *http.Request) {
		// Never inherit a public-route exception or development no-auth mode.
		if token := os.Getenv("APTEVA_APP_TOKEN"); token == "" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := ValidateComponentProtocol(protocol); err != nil {
			http.Error(w, "invalid component protocol", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ComponentDescriptor{Protocol: protocol})
	})
}
