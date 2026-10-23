package sdk

import (
	"fmt"
	"unicode/utf8"
)

// AgentSetup supplies presentation hints for the new-agent app picker.
// A recommendation never installs an app, selects it, or grants permissions.
type AgentSetup struct {
	Recommended bool   `yaml:"recommended,omitempty" json:"recommended,omitempty"`
	Reason      string `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// ValidateAgentSetup is shared by manifest and registry metadata loading.
func ValidateAgentSetup(setup *AgentSetup) error {
	if setup != nil && utf8.RuneCountInString(setup.Reason) > 300 {
		return fmt.Errorf("agent_setup.reason must be at most 300 characters")
	}
	return nil
}
