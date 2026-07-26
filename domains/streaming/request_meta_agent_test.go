package streaming

import (
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/telemetry" //nolint:depguard // test-only alias to /telemetry root package
)

// TestFillAttemptMeta_AgentNameSemanticFallback verifies the 2026-07-27
// change: when header-based ExtractAgentName returns "unknown" (e.g. the
// client sends a generic Go HTTP client User-Agent) but the system prompt
// self-identifies as a known agent, fillAttemptMeta must backfill the
// canonical agent name via telemetry.DetectAgentFromSystemPrompt.
//
// Without this fallback, most real-world AI agent traffic shows up in
// request_logs.agent_name as "unknown", which makes the client-type
// dashboard useless.
func TestFillAttemptMeta_AgentNameSemanticFallback(t *testing.T) {
	tests := []struct {
		name         string
		userAgent    string
		systemPrompt string
		wantAgent    string
	}{
		{
			name:         "go-http-client UA but prompt says ZCode — override wins",
			userAgent:    "Go-http-client/1.1",
			systemPrompt: "You are ZCode (Claude Code) — ACC 团队标准",
			wantAgent:    "zcode",
		},
		{
			name:         "python-requests UA but prompt says opencode — override wins",
			userAgent:    "python-requests/2.28",
			systemPrompt: "You are opencode, an interactive CLI tool.",
			wantAgent:    "opencode",
		},
		{
			name:         "header is unknown but prompt says claude-code — fallback fires",
			userAgent:    "axios/1.0",
			systemPrompt: "You are Claude Code by Anthropic.",
			wantAgent:    "claude-code",
		},
		{
			name:         "header is unknown but prompt says cursor — fallback fires",
			userAgent:    "axios/1.0",
			systemPrompt: "You are Cursor IDE, an AI-powered code editor.",
			wantAgent:    "cursor",
		},
		{
			name:         "header is unknown but prompt says cline — fallback fires",
			userAgent:    "axios/1.0",
			systemPrompt: "You are Cline, an AI coding assistant.",
			wantAgent:    "cline",
		},
		{
			name:         "header is unknown but prompt says aider — fallback fires",
			userAgent:    "axios/1.0",
			systemPrompt: "You are aider, an AI pair programmer.",
			wantAgent:    "aider",
		},
		{
			name:         "header is unknown but prompt says kiro — fallback fires",
			userAgent:    "axios/1.0",
			systemPrompt: "You are kiro, an AI IDE.",
			wantAgent:    "kiro",
		},
		{
			name:         "header is unknown but prompt says windsurf — fallback fires",
			userAgent:    "axios/1.0",
			systemPrompt: "You are Windsurf Editor AI assistant.",
			wantAgent:    "windsurf",
		},
		{
			name:         "header is unknown but prompt says roocode — fallback fires",
			userAgent:    "axios/1.0",
			systemPrompt: "You are RooCode, an AI-powered coding assistant.",
			wantAgent:    "roocode",
		},
		{
			name:         "header already identifies Claude Code — no fallback needed",
			userAgent:    "Claude-Code/1.0",
			systemPrompt: "You are some unrelated prompt here.",
			wantAgent:    "claude-code",
		},
		{
			name:         "go-http-client UA, no system prompt — stays go-client",
			userAgent:    "Go-http-client/1.1",
			systemPrompt: "",
			wantAgent:    "go-client",
		},
		{
			name:         "go-http-client UA, generic system prompt — stays go-client",
			userAgent:    "Go-http-client/1.1",
			systemPrompt: "You are a helpful assistant.",
			wantAgent:    "go-client",
		},
		{
			name:         "truly unknown UA, generic system prompt — stays unknown",
			userAgent:    "SomeRandomClient/9.9",
			systemPrompt: "You are a helpful assistant.",
			wantAgent:    "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset registry to default built-ins to avoid cross-test pollution.
			telemetry.ResetAgentPatterns()

			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			req.Header.Set("User-Agent", tt.userAgent)

			h := &ChatHandler{}
			meta := &requestAttemptMeta{SystemPrompt: tt.systemPrompt}

			// fillAttemptMeta calls telemetry.ExtractAgentName/Type and our
			// new semantic-fallback logic.
			h.fillAttemptMeta(req, nil, meta)

			if meta.AgentName != tt.wantAgent {
				t.Errorf("AgentName = %q, want %q", meta.AgentName, tt.wantAgent)
			}
		})
	}
}

// TestFillAttemptMeta_SystemPromptFalsyDoesNotCall verifies the guard:
// when meta.SystemPrompt is empty, fillAttemptMeta must not crash and
// must not change AgentName away from its header-based value.
func TestFillAttemptMeta_SystemPromptFalsyDoesNotCall(t *testing.T) {
	telemetry.ResetAgentPatterns()

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("User-Agent", "OpenCode/2.0")

	h := &ChatHandler{}
	meta := &requestAttemptMeta{SystemPrompt: ""}

	h.fillAttemptMeta(req, nil, meta)

	if meta.AgentName != "opencode" {
		t.Errorf("AgentName = %q, want %q", meta.AgentName, "opencode")
	}
}


func TestShouldOverrideAgentName(t *testing.T) {
	tests := []struct {
		name         string
		headerName   string
		systemPrompt string
		want         bool
	}{
		// Override cases
		{"empty + system prompt", "", "You are Claude Code", true},
		{"unknown + system prompt", "unknown", "You are Claude Code", true},
		{"go-client + system prompt", "go-client", "You are opencode", true},
		{"python-client + system prompt", "python-client", "You are ZCode", true},
		{"curl + system prompt", "curl", "You are Cursor", true},
		{"postman + system prompt", "postman", "You are RooCode", true},
		{"insomnia + system prompt", "insomnia", "You are aider", true},
		// No override (specific agent header takes precedence)
		{"claude-code header + system prompt", "claude-code", "You are opencode", false},
		{"opencode header + system prompt", "opencode", "You are Claude Code", false},
		{"zcode header + system prompt", "zcode", "You are Claude Code", false},
		{"cursor header + system prompt", "cursor", "You are claude-code", false},
		// No override (no system prompt)
		{"unknown + empty prompt", "unknown", "", false},
		{"go-client + empty prompt", "go-client", "", false},
		// No override (whitespace-only prompt)
		{"unknown + whitespace", "unknown", "   \n\t  ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldOverrideAgentName(tt.headerName, tt.systemPrompt)
			if got != tt.want {
				t.Errorf("shouldOverrideAgentName(%q, %q) = %v, want %v",
					tt.headerName, tt.systemPrompt, got, tt.want)
			}
		})
	}
}
