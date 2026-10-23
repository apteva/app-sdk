package sdk

import "fmt"

// HostUI is an optional, transient presentation request for a mounted UI
// surface. Hosts decide foreground eligibility and may ignore unsupported
// settings. It never changes user preferences or assistant session state.
type HostUI struct {
	ChatAssistant *HostUIChatAssistant `yaml:"chat_assistant,omitempty" json:"chat_assistant,omitempty"`
}

type HostUIChatAssistant struct {
	Launcher ChatAssistantLauncher `yaml:"launcher,omitempty" json:"launcher,omitempty"`
}

type ChatAssistantLauncher string

const (
	ChatAssistantLauncherAuto            ChatAssistantLauncher = "auto"
	ChatAssistantLauncherHiddenOnCompact ChatAssistantLauncher = "hidden_on_compact"
	ChatAssistantLauncherHidden          ChatAssistantLauncher = "hidden"
)

func validateHostUI(ui *HostUI) error {
	if ui == nil || ui.ChatAssistant == nil {
		return nil
	}
	switch ui.ChatAssistant.Launcher {
	case "", ChatAssistantLauncherAuto, ChatAssistantLauncherHiddenOnCompact, ChatAssistantLauncherHidden:
		return nil
	default:
		return fmt.Errorf("chat_assistant.launcher %q invalid (use auto, hidden_on_compact, or hidden)", ui.ChatAssistant.Launcher)
	}
}
