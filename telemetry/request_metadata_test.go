package telemetry

import (
	"net/http/httptest"
	"testing"
)

func TestExtractClientIP(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		expectedIP string
	}{
		{
			name:       "X-Real-IP present",
			headers:    map[string]string{"X-Real-IP": "203.0.113.5"},
			remoteAddr: "10.0.0.1:12345",
			expectedIP: "203.0.113.5",
		},
		{
			name:       "X-Forwarded-For with single IP",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.10"},
			remoteAddr: "10.0.0.1:12345",
			expectedIP: "203.0.113.10",
		},
		{
			name:       "X-Forwarded-For with chain",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.20, 10.0.0.5, 192.168.1.1"},
			remoteAddr: "10.0.0.1:12345",
			expectedIP: "203.0.113.20",
		},
		{
			name: "X-Real-IP takes precedence over X-Forwarded-For",
			headers: map[string]string{
				"X-Real-IP":       "203.0.113.30",
				"X-Forwarded-For": "203.0.113.40",
			},
			remoteAddr: "10.0.0.1:12345",
			expectedIP: "203.0.113.30",
		},
		{
			name:       "Fallback to RemoteAddr",
			headers:    map[string]string{},
			remoteAddr: "192.168.1.100:54321",
			expectedIP: "192.168.1.100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			got := ExtractClientIP(req)
			if got != tt.expectedIP {
				t.Errorf("ExtractClientIP() = %v, want %v", got, tt.expectedIP)
			}
		})
	}
}

func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		name     string
		apiKey   string
		expected string
	}{
		{
			name:     "Standard OpenAI key",
			apiKey:   "sk-1234abcd5678efgh9012ijkl",
			expected: "sk-1234a***",
		},
		{
			name:     "Short key",
			apiKey:   "abc123",
			expected: "***",
		},
		{
			name:     "Empty key",
			apiKey:   "",
			expected: "",
		},
		{
			name:     "Exactly 8 chars",
			apiKey:   "12345678",
			expected: "***",
		},
		{
			name:     "9 chars",
			apiKey:   "123456789",
			expected: "12345678***",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaskAPIKey(tt.apiKey)
			if got != tt.expected {
				t.Errorf("MaskAPIKey(%q) = %q, want %q", tt.apiKey, got, tt.expected)
			}
		})
	}
}

func TestExtractAgentName(t *testing.T) {
	tests := []struct {
		name          string
		headers       map[string]string
		expectedAgent string
	}{
		{
			name:          "X-Agent-Name header present",
			headers:       map[string]string{"X-Agent-Name": "my-custom-agent"},
			expectedAgent: "my-custom-agent",
		},
		{
			name:          "Claude Code User-Agent",
			headers:       map[string]string{"User-Agent": "Claude-Code/1.0"},
			expectedAgent: "claude-code",
		},
		{
			name:          "OpenCode User-Agent",
			headers:       map[string]string{"User-Agent": "OpenCode/2.0"},
			expectedAgent: "opencode",
		},
		{
			name:          "ZCode User-Agent",
			headers:       map[string]string{"User-Agent": "ZCode/1.0"},
			expectedAgent: "zcode",
		},
		{
			name:          "Codex User-Agent",
			headers:       map[string]string{"User-Agent": "Codex-CLI/0.1"},
			expectedAgent: "codex",
		},
		{
			name:          "Cursor User-Agent",
			headers:       map[string]string{"User-Agent": "Cursor/1.5"},
			expectedAgent: "cursor",
		},
		{
			name:          "RooCode User-Agent",
			headers:       map[string]string{"User-Agent": "RooCode/3.0"},
			expectedAgent: "roocode",
		},
		{
			name:          "Windsurf User-Agent",
			headers:       map[string]string{"User-Agent": "Windsurf/1.0"},
			expectedAgent: "windsurf",
		},
		{
			name:          "Zed User-Agent",
			headers:       map[string]string{"User-Agent": "Zed/2.0"},
			expectedAgent: "zed",
		},
		{
			name:          "Copilot User-Agent",
			headers:       map[string]string{"User-Agent": "GitHub-Copilot/1.0"},
			expectedAgent: "copilot",
		},
		{
			name:          "Python client",
			headers:       map[string]string{"User-Agent": "python-requests/2.28.0"},
			expectedAgent: "python-client",
		},
		{
			name:          "Curl",
			headers:       map[string]string{"User-Agent": "curl/7.88.1"},
			expectedAgent: "curl",
		},
		{
			name:          "No User-Agent",
			headers:       map[string]string{},
			expectedAgent: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			got := ExtractAgentName(req)
			if got != tt.expectedAgent {
				t.Errorf("ExtractAgentName() = %v, want %v", got, tt.expectedAgent)
			}
		})
	}
}

func TestDetectAgentFromSystemPrompt(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{
			name:   "empty prompt",
			prompt: "",
			want:   "",
		},
		{
			name:   "claude-code standard",
			prompt: "You are Claude Code by Anthropic. You are an AI assistant.",
			want:   "claude-code",
		},
		{
			name:   "claude-code hyphen form",
			prompt: "You are claude-code, a coding agent.",
			want:   "claude-code",
		},
		{
			name:   "opencode",
			prompt: "You are opencode, an interactive CLI tool that helps users with software engineering tasks.",
			want:   "opencode",
		},
		{
			name:   "zcode",
			prompt: "You are ZCode (Claude Code) — ACC 团队标准系统提示",
			want:   "zcode",
		},
		{
			name:   "vscode",
			prompt: "You are an AI assistant in Visual Studio Code.",
			want:   "vscode",
		},
		{
			name:   "cursor",
			prompt: "You are an AI assistant in Cursor IDE. I help you write code.",
			want:   "cursor",
		},
		{
			name:   "codex CLI",
			prompt: "You are OpenAI Codex CLI, a coding agent.",
			want:   "codex",
		},
		{
			name:   "no match — generic assistant",
			prompt: "You are a helpful assistant.",
			want:   "",
		},
		{
			name:   "no match — empty pattern",
			prompt: "Just some random conversation text without agent identification.",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectAgentFromSystemPrompt(tt.prompt)
			if got != tt.want {
				t.Errorf("DetectAgentFromSystemPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractAgentType(t *testing.T) {
	tests := []struct {
		name         string
		headers      map[string]string
		expectedType string
	}{
		{
			name:         "X-Agent-Type header present",
			headers:      map[string]string{"X-Agent-Type": "internal"},
			expectedType: "internal",
		},
		{
			name:         "CLI - curl",
			headers:      map[string]string{"User-Agent": "curl/7.88.1"},
			expectedType: "cli",
		},
		{
			name:         "CLI - claude-code",
			headers:      map[string]string{"User-Agent": "Claude-Code/1.0"},
			expectedType: "cli",
		},
		{
			name:         "CLI - opencode",
			headers:      map[string]string{"User-Agent": "OpenCode/2.0"},
			expectedType: "cli",
		},
		{
			name:         "CLI - zcode",
			headers:      map[string]string{"User-Agent": "ZCode/1.0"},
			expectedType: "cli",
		},
		{
			name:         "CLI - codex",
			headers:      map[string]string{"User-Agent": "Codex-CLI/0.1"},
			expectedType: "cli",
		},
		{
			name:         "CLI - roocode",
			headers:      map[string]string{"User-Agent": "RooCode/3.0"},
			expectedType: "cli",
		},
		{
			name:         "CLI - windsurf",
			headers:      map[string]string{"User-Agent": "Windsurf/1.0"},
			expectedType: "cli",
		},
		{
			name:         "CLI - copilot",
			headers:      map[string]string{"User-Agent": "GitHub-Copilot/1.0"},
			expectedType: "cli",
		},
		{
			name:         "API - python",
			headers:      map[string]string{"User-Agent": "python-requests/2.28.0"},
			expectedType: "api",
		},
		{
			name:         "API - Go",
			headers:      map[string]string{"User-Agent": "Go-http-client/1.1"},
			expectedType: "api",
		},
		{
			name:         "Bot - crawler",
			headers:      map[string]string{"User-Agent": "Googlebot/2.1"},
			expectedType: "bot",
		},
		{
			name:         "Mobile - iPhone",
			headers:      map[string]string{"User-Agent": "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0)"},
			expectedType: "mobile",
		},
		{
			name:         "Mobile - Android",
			headers:      map[string]string{"User-Agent": "Mozilla/5.0 (Linux; Android 13)"},
			expectedType: "mobile",
		},
		{
			name:         "Web - Chrome",
			headers:      map[string]string{"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0"},
			expectedType: "web",
		},
		{
			name:         "Unknown",
			headers:      map[string]string{"User-Agent": "CustomClient/1.0"},
			expectedType: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			got := ExtractAgentType(req)
			if got != tt.expectedType {
				t.Errorf("ExtractAgentType() = %v, want %v", got, tt.expectedType)
			}
		})
	}
}

func TestNewRequestMetadata(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.100:54321"
	req.Header.Set("X-Real-IP", "203.0.113.5")
	req.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	req.Header.Set("User-Agent", "Claude-Code/1.0")

	meta := NewRequestMetadata(req)

	if meta.ClientIP != "203.0.113.5" {
		t.Errorf("ClientIP = %v, want 203.0.113.5", meta.ClientIP)
	}

	if meta.ClientForwardedFor != "203.0.113.5, 10.0.0.1" {
		t.Errorf("ClientForwardedFor = %v, want '203.0.113.5, 10.0.0.1'", meta.ClientForwardedFor)
	}

	if meta.AgentName != "claude-code" {
		t.Errorf("AgentName = %v, want claude-code", meta.AgentName)
	}

	if meta.AgentType != "cli" {
		t.Errorf("AgentType = %v, want cli", meta.AgentType)
	}
}

func TestExtractForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.1, 10.0.0.1, 192.168.1.1")

	got := ExtractForwardedFor(req)
	expected := "203.0.113.1, 10.0.0.1, 192.168.1.1"

	if got != expected {
		t.Errorf("ExtractForwardedFor() = %v, want %v", got, expected)
	}
}

func TestAPIKeyFingerprint(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		want    string
		wantLen int
	}{
		{name: "empty", key: "", want: "", wantLen: 0},
		{name: "whitespace only", key: "   ", want: "", wantLen: 0},
		{name: "typical key", key: "sk-1234abcd5678efgh", wantLen: 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := APIKeyFingerprint(tt.key)
			if tt.want != "" && got != tt.want {
				t.Errorf("APIKeyFingerprint(%q) = %q, want %q", tt.key, got, tt.want)
			}
			if len(got) != tt.wantLen {
				t.Errorf("APIKeyFingerprint(%q) len = %d, want %d (got=%q)", tt.key, len(got), tt.wantLen, got)
			}
		})
	}

	// Determinism: same input → same output
	a := APIKeyFingerprint("sk-test-key-1")
	b := APIKeyFingerprint("sk-test-key-1")
	if a != b {
		t.Errorf("APIKeyFingerprint not deterministic: %q vs %q", a, b)
	}
	// Distinct inputs → distinct outputs
	c := APIKeyFingerprint("sk-test-key-2")
	if a == c {
		t.Errorf("APIKeyFingerprint collision for distinct inputs: %q == %q", a, c)
	}
	// Whitespace trimming
	d := APIKeyFingerprint("  sk-test-key-1  ")
	if d != a {
		t.Errorf("APIKeyFingerprint should trim whitespace: %q vs %q", d, a)
	}
}

func TestRegisterAgentPattern(t *testing.T) {
	defer ResetAgentPatterns()

	// Custom agent not yet registered — should not match
	if got := DetectAgentFromSystemPrompt("you are my-custom-agent/1.0"); got != "" {
		t.Fatalf("before registration: DetectAgentFromSystemPrompt() = %q, want %q", got, "")
	}

	RegisterAgentPattern("my-agent", "my-custom-agent", "my agent")

	// Now it should match
	if got := DetectAgentFromSystemPrompt("you are my-custom-agent/1.0"); got != "my-agent" {
		t.Errorf("after registration: DetectAgentFromSystemPrompt() = %q, want %q", got, "my-agent")
	}

	// Second pattern should also match
	if got := DetectAgentFromSystemPrompt("i am my agent"); got != "my-agent" {
		t.Errorf("second pattern: DetectAgentFromSystemPrompt() = %q, want %q", got, "my-agent")
	}

	// Built-in patterns should still work
	if got := DetectAgentFromSystemPrompt("You are Claude Code"); got != "claude-code" {
		t.Errorf("built-in after registration: DetectAgentFromSystemPrompt() = %q, want %q", got, "claude-code")
	}
}

func TestRegisterAgentPattern_DuplicateName(t *testing.T) {
	defer ResetAgentPatterns()

	RegisterAgentPattern("custom", "pattern-one")
	RegisterAgentPattern("custom", "pattern-two")

	// Both patterns for the same name should match
	if got := DetectAgentFromSystemPrompt("pattern-one here"); got != "custom" {
		t.Errorf("pattern-one: DetectAgentFromSystemPrompt() = %q, want %q", got, "custom")
	}
	if got := DetectAgentFromSystemPrompt("pattern-two here"); got != "custom" {
		t.Errorf("pattern-two: DetectAgentFromSystemPrompt() = %q, want %q", got, "custom")
	}
}

func TestRegisterAgentPattern_EmptyName(t *testing.T) {
	defer ResetAgentPatterns()

	// Should not panic or register anything
	RegisterAgentPattern("", "some-pattern")
	RegisterAgentPattern("valid", "")

	builtins := DetectAgentFromSystemPrompt("You are Claude Code")
	if builtins != "claude-code" {
		t.Errorf("builtins not intact: %q", builtins)
	}
}

func TestResetAgentPatterns(t *testing.T) {
	RegisterAgentPattern("ephemeral", "temp-pattern")

	if got := DetectAgentFromSystemPrompt("temp-pattern here"); got != "ephemeral" {
		t.Fatalf("before reset: DetectAgentFromSystemPrompt() = %q, want %q", got, "ephemeral")
	}

	ResetAgentPatterns()

	if got := DetectAgentFromSystemPrompt("temp-pattern here"); got != "" {
		t.Errorf("after reset: DetectAgentFromSystemPrompt() = %q, want %q", got, "")
	}

	// Built-ins should still work after reset
	if got := DetectAgentFromSystemPrompt("You are Claude Code"); got != "claude-code" {
		t.Errorf("built-in after reset: DetectAgentFromSystemPrompt() = %q, want %q", got, "claude-code")
	}
}

func TestEnrichAgentNameFromSystemPrompt(t *testing.T) {
	tests := []struct {
		name         string
		headerName   string
		systemPrompt string
		want         string
	}{
		{
			name:         "header name present and known — keep it",
			headerName:   "claude-code",
			systemPrompt: "You are ZCode (Claude Code)",
			want:         "claude-code",
		},
		{
			name:         "header is 'unknown' — fallback to semantic",
			headerName:   "unknown",
			systemPrompt: "You are ZCode (Claude Code)",
			want:         "zcode",
		},
		{
			name:         "header is empty — fallback to semantic",
			headerName:   "",
			systemPrompt: "You are opencode, an interactive CLI tool",
			want:         "opencode",
		},
		{
			name:         "header known, no system prompt match",
			headerName:   "claude-code",
			systemPrompt: "",
			want:         "claude-code",
		},
		{
			name:         "both empty — empty result",
			headerName:   "",
			systemPrompt: "",
			want:         "",
		},
		{
			name:         "header unknown, no system prompt match",
			headerName:   "unknown",
			systemPrompt: "random text without agent",
			want:         "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EnrichAgentNameFromSystemPrompt(tt.headerName, tt.systemPrompt)
			if got != tt.want {
				t.Errorf("EnrichAgentNameFromSystemPrompt(%q, %q) = %q, want %q",
					tt.headerName, tt.systemPrompt, got, tt.want)
			}
		})
	}
}
