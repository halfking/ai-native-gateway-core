package clienttype

import "strings"

const Unknown = "unknown"

// Normalize bounds client-type values used in Redis holders and Prometheus labels.
func Normalize(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "cursor", "claude-code", "opencode", "zcode", "codex", "roocode",
		"vscode", "copilot", "windsurf", "zed", "jetbrains", Unknown:
		return value
	default:
		return Unknown
	}
}
