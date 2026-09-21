package clienttype

import "strings"

const Unknown = "unknown"

// Normalize bounds client-type values used in Redis holders and Prometheus labels.
//
// 2026-09-21 audit: added "minimax-code" and "deepseek-code" — the two
// domestic (CN) coding-agent clients that emit a recognisable body shape
// distinct from "zcode". The string is also the canonical value returned
// by telemetry.DetectAgentFromSystemPrompt and the body-level
// ExtractClientTypeFromBody so all three detection paths converge on the
// same Prometheus label cardinality.
func Normalize(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "cursor", "claude-code", "opencode", "zcode", "codex", "roocode",
		"vscode", "copilot", "windsurf", "zed", "jetbrains",
		// 2026-09-21: domestic coding-agent clients
		"minimax-code", "deepseek-code",
		Unknown:
		return value
	default:
		return Unknown
	}
}
