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
		// 2026-09-21 audit: domestic coding-agent clients.
		{"minimax-code with slash", "MiniMax-Code/1.0.0 (macos; arm64)", "minimax-code"},
		// 2026-10-29 production verification: wire-real bare form (211 logs).
		{"minimax-code bare compound", "MiniMaxCode", "minimax-code"},
		{"minimax-code bare compound with version", "MiniMaxCode/1.2.0", "minimax-code"},
		{"deepseek-code with slash", "DeepSeek-Code/1.0.0 (darwin; arm64)", "deepseek-code"},
		{"deepseek-cli", "deepseek-cli/0.3.1", "deepseek-code"},
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

// TestExtractClientType_ClaudeCodeWireForms（2026-10-29 identification round）：
// Claude Code 真实线材 UA 是 claude-cli/{ver}（官方规范 + 线材核实），此前
// 两表只认 claude-code 字样 ⇒ RISK-6 加的 claude-code↔claude 原生对在
// header/UA 首选路径上永不命中（RISK-7）。
func TestExtractClientType_ClaudeCodeWireForms(t *testing.T) {
	tests := []struct {
		name string
		ua   string
		beta string
		want string
	}{
		{"real claude-cli UA", "claude-cli/2.1.75 (external, cli)", "", "claude-code"},
		{"claude-cli newer form", "claude-cli/2.1.300 (external, sdk-cli)", "", "claude-code"},
		{"archive claude-code UA", "Claude-Code/1.0.0", "", "claude-code"},
		// anthropic-beta 旗标（vendor 自家命名空间）：UA 缺失时仍可识别
		{"beta flag only", "", "claude-code-20250219,oauth-2025-04-20", "claude-code"},
		// 负臂：普通 Anthropic SDK 不带 claude-code 旗标 ⇒ 不得识别
		{"plain anthropic sdk", "anthropic-sdk-python/0.39.0", "interleaved-thinking-2025-05-14", ""},
		// 负臂：形近词不得命中
		{"claude-clix lookalike", "claude-clix/1.0", "", ""},
		{"claude-coder lookalike", "claude-coder/1.0", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/messages", nil)
			if tt.ua != "" {
				req.Header.Set("User-Agent", tt.ua)
			}
			if tt.beta != "" {
				req.Header.Set("Anthropic-Beta", tt.beta)
			}
			if got := extractClientType(req); got != tt.want {
				t.Errorf("extractClientType(ua=%q beta=%q) = %q, want %q", tt.ua, tt.beta, got, tt.want)
			}
		})
	}
}

// TestExtractClientType_BoundaryLookalikes（2026-10-29）：裸 token 的形近
// 词防线 —— HasToken 双边界后，字母数字延续不得命中（此前 ZCodex 会先命中
// zcode、MiniMaxCoder 会命中 minimaxcode）。
func TestExtractClientType_BoundaryLookalikes(t *testing.T) {
	tests := []struct {
		ua   string
		want string
	}{
		{"MiniMaxCoder/1.0", ""},
		{"ZCodex/2.0", ""}, // 既不是 zcode 也不是 codex
		{"visualized/2.0", ""},
		// 真实形态回归
		{"MiniMaxCode", "minimax-code"},
		{"ZCode/3.16.3 ai-sdk/provider-utils/4.0.39 runtime/node.js/24", "zcode"},
		{"MiniMax-Code/1.0.0 (macos; arm64)", "minimax-code"},
		{"DeepSeek-Code/1.0.0 (darwin; arm64)", "deepseek-code"},
		{"deepseek-cli/0.3.1", "deepseek-code"},
	}
	for _, tt := range tests {
		t.Run(tt.ua, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			req.Header.Set("User-Agent", tt.ua)
			if got := extractClientType(req); got != tt.want {
				t.Errorf("extractClientType(%q) = %q, want %q", tt.ua, got, tt.want)
			}
		})
	}
}
