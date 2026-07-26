package streaming

import (
	"net/http/httptest"
	"testing"
)

func TestExtractClientType_UserAgent(t *testing.T) {
	tests := []struct {
		name string
		ua   string
		want string
	}{
		{"cursor with slash", "Cursor/1.5.0", "cursor"},
		{"claude-code with slash", "Claude-Code/1.0.0", "claude-code"},
		{"opencode with slash", "OpenCode/0.1", "opencode"},
		{"zcode with slash", "ZCode/1.0", "zcode"},
		{"codex with slash", "Codex-CLI/0.1", "codex"},
		{"roocode with slash", "RooCode/3.0", "roocode"},
		{"vscode with slash", "VSCode/1.85", "vscode"},
		{"copilot with slash", "GitHub-Copilot/1.0", "copilot"},
		{"windsurf with slash", "Windsurf/1.0", "windsurf"},
		{"zed with slash", "Zed/2.0", "zed"},
		{"jetbrains", "JetBrains/2024.1", "jetbrains"},
		{"unknown agent", "SomeRandomClient/1.0", ""},
		{"empty user-agent", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			req.Header.Set("User-Agent", tt.ua)
			got := extractClientType(req)
			if got != tt.want {
				t.Errorf("extractClientType(User-Agent=%q) = %q, want %q", tt.ua, got, tt.want)
			}
		})
	}
}

func TestExtractClientType_XGwHeader(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Gw-Client-Type", "my-custom-agent")
	got := extractClientType(req)
	if got != "my-custom-agent" {
		t.Errorf("extractClientType(X-Gw-Client-Type=my-custom-agent) = %q, want %q", got, "my-custom-agent")
	}
}

func TestExtractClientTypeWithPrompt_HeaderTakesPriority(t *testing.T) {
	// X-Gw-Client-Type should win over system prompt
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Gw-Client-Type", "explicit-agent")
	got := extractClientTypeWithPrompt(req, "You are ZCode, an AI assistant")
	if got != "explicit-agent" {
		t.Errorf("extractClientTypeWithPrompt() = %q, want %q (header should win)", got, "explicit-agent")
	}
}

func TestExtractClientTypeWithPrompt_UserAgentWins(t *testing.T) {
	// User-Agent should win over system prompt
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("User-Agent", "Cursor/1.5")
	got := extractClientTypeWithPrompt(req, "You are ZCode, an AI assistant")
	if got != "cursor" {
		t.Errorf("extractClientTypeWithPrompt() = %q, want %q (UA should win)", got, "cursor")
	}
}

func TestExtractClientTypeWithPrompt_SemanticFallback(t *testing.T) {
	tests := []struct {
		name   string
		ua     string
		prompt string
		want   string
	}{
		{
			name:   "opencode from system prompt",
			ua:     "GenericHTTP/1.0",
			prompt: "You are opencode, an interactive CLI tool for software engineering.",
			want:   "opencode",
		},
		{
			name:   "zcode from system prompt",
			ua:     "axios/1.0",
			prompt: "You are ZCode (Claude Code) — ACC 团队系统提示",
			want:   "zcode",
		},
		{
			name:   "claude-code from system prompt",
			ua:     "python-requests/2.28",
			prompt: "You are Claude Code by Anthropic. You are a coding assistant.",
			want:   "claude-code",
		},
		{
			name:   "no match falls through empty",
			ua:     "GenericHTTP/1.0",
			prompt: "You are a helpful assistant.",
			want:   "",
		},
		{
			name:   "empty prompt stays empty",
			ua:     "GenericHTTP/1.0",
			prompt: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			req.Header.Set("User-Agent", tt.ua)
			got := extractClientTypeWithPrompt(req, tt.prompt)
			if got != tt.want {
				t.Errorf("extractClientTypeWithPrompt(UA=%q, prompt=%q) = %q, want %q",
					tt.ua, tt.prompt, got, tt.want)
			}
		})
	}
}
