package autoroute

// classifier_ide.go — IDE client fingerprint detection (需求 #1 的一部分)
//
// 识别来自编程 IDE 的请求，作为编程任务的强信号。
// ClientType 从 relay/auto_route.go 的 extractSignalsForAuto() 提取。

// isIDEClient reports whether the given client type string represents
// a known IDE or code-centric client.
//
// Recognized clients (case-insensitive):
//   - "cursor"       — Cursor IDE (Anysphere)
//   - "claude-code"  — Claude Code (Anthropic)
//   - "roocode"      — Roo Code (variant)
//   - "vscode"       — Visual Studio Code (with AI extension)
//   - "copilot"      — GitHub Copilot
//   - "windsurf"     — Windsurf IDE
//   - "zed"          — Zed editor
//   - "jetbrains"    — JetBrains IDEs (IntelliJ/PyCharm/...)
//
// Empty string or unknown type returns false.
func isIDEClient(clientType string) bool {
	if clientType == "" {
		return false
	}
	// Case-insensitive match (ClientType is already lowercased in relay)
	switch lowerASCII(clientType) {
	case "cursor", "claude-code", "roocode", "vscode", "copilot", "windsurf", "zed", "jetbrains":
		return true
	default:
		return false
	}
}
