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
	for _, tt := range []struct {
		header string
		want   string
	}{
		{" Cursor ", "cursor"},
		{"CLAUDE-CODE", "claude-code"},
		{"my-custom-agent", "unknown"},
		{"cursor|other", "unknown"},
	} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("X-Gw-Client-Type", tt.header)
		if got := extractClientType(req); got != tt.want {
			t.Errorf("extractClientType(X-Gw-Client-Type=%q) = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestExtractClientTypeWithPrompt_HeaderTakesPriority(t *testing.T) {
	// X-Gw-Client-Type should win over system prompt
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Gw-Client-Type", "explicit-agent")
	got := extractClientTypeWithPrompt(req, "You are ZCode, an AI assistant")
	if got != "unknown" {
		t.Errorf("extractClientTypeWithPrompt() = %q, want %q (unknown explicit header should stay bounded)", got, "unknown")
	}
}

// TestClientTokenOf_Defaults 验证空 userKey / 空 clientType 的回退规则
// 设计要求（2026-07-27 Token 资源管理方案）：
//   - 空 userKey  → "anon"
//   - 空 clientType → "unknown"
func TestClientTokenOf_Defaults(t *testing.T) {
	tests := []struct {
		name       string
		userKey    string
		clientType string
		want       string
	}{
		{"both empty", "", "", "anon|unknown"},
		{"empty userKey", "", "cursor", "anon|cursor"},
		{"empty clientType", "alice", "", "alice|unknown"},
		{"whitespace userKey", "   ", "claude-code", "   |claude-code"},
		{"whitespace clientType", "bob", "   ", "bob|unknown"},
		{"custom clientType", "bob", "my-agent", "bob|unknown"},
		{"delimiter clientType", "bob", "cursor|other", "bob|unknown"},
		{"normal", "alice", "cursor", "alice|cursor"},
		{"normal other", "bob", "claude-code", "bob|claude-code"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClientTokenOf(tt.userKey, tt.clientType)
			if got != tt.want {
				t.Errorf("ClientTokenOf(%q, %q) = %q, want %q",
					tt.userKey, tt.clientType, got, tt.want)
			}
		})
	}
}

// TestClientTokenOf_StableFormat 验证同一输入多次拼接结果一致
// 这是 Redis key 生成正确性的基础（不同请求对同一客户端必须 hash 到同一 key）。
func TestClientTokenOf_StableFormat(t *testing.T) {
	inputs := [][2]string{
		{"alice", "cursor"},
		{"bob", "claude-code"},
		{"", "unknown"},
		{"anon", ""},
		{"user-with-dash", "jetbrains"},
		{"123", "vscode"},
	}
	for _, in := range inputs {
		a := ClientTokenOf(in[0], in[1])
		b := ClientTokenOf(in[0], in[1])
		if a != b {
			t.Errorf("ClientTokenOf(%q, %q) not stable: %q vs %q", in[0], in[1], a, b)
		}
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
