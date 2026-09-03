package telemetry

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAgentSignatureCursor verifies the Cursor client is correctly
// identified by both the User-Agent header (header-based extraction) and
// the canonical system-prompt self-description (semantic fallback).
//
// Cursor's documented User-Agent is "Cursor/{version}" (case-insensitive).
// Its system prompt starts with "You are an AI coding assistant, powered
// by Composer. You operate in Cursor." — this is the canonical signature
// shipped with the Cursor IDE/CLI product.
func TestAgentSignatureCursor(t *testing.T) {
	ResetAgentPatterns()
	t.Cleanup(ResetAgentPatterns)

	t.Run("header path", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("User-Agent", "Cursor/0.42.5 (macos; arm64) AppleWebKit/537.36")
		if got := ExtractAgentName(req); got != "cursor" {
			t.Fatalf("ExtractAgentName = %q, want %q", got, "cursor")
		}
	})

	t.Run("system prompt path", func(t *testing.T) {
		prompt := "You are an AI coding assistant, powered by Composer. You operate in Cursor."
		if got := DetectAgentFromSystemPrompt(prompt); got != "cursor" {
			t.Fatalf("DetectAgentFromSystemPrompt = %q, want %q", got, "cursor")
		}
	})

	t.Run("system prompt with composer variant", func(t *testing.T) {
		// Cursor's composer flavour sometimes omits "You operate in Cursor"
		// but still includes the "an AI assistant ... in Cursor" line.
		// The registry pattern "operate in cursor" must still match.
		prompt := "You are an AI coding assistant, powered by Composer. You operate in Cursor IDE."
		if got := DetectAgentFromSystemPrompt(prompt); got != "cursor" {
			t.Fatalf("DetectAgentFromSystemPrompt = %q, want %q", got, "cursor")
		}
	})
}

// TestAgentSignatureZCode verifies the ZCode CLI is correctly
// identified. ZCode's User-Agent is "ZCode/{version}" and its system
// prompt starts with "You are ZCode, an interactive coding agent. You
// are an agent for ZCode CLI."
func TestAgentSignatureZCode(t *testing.T) {
	ResetAgentPatterns()
	t.Cleanup(ResetAgentPatterns)

	t.Run("header path", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("User-Agent", "ZCode/1.2.3 (darwin; arm64)")
		if got := ExtractAgentName(req); got != "zcode" {
			t.Fatalf("ExtractAgentName = %q, want %q", got, "zcode")
		}
	})

	t.Run("system prompt path", func(t *testing.T) {
		prompt := "You are ZCode, an interactive coding agent\nYou are an agent for ZCode CLI. "
		if got := DetectAgentFromSystemPrompt(prompt); got != "zcode" {
			t.Fatalf("DetectAgentFromSystemPrompt = %q, want %q", got, "zcode")
		}
	})

	t.Run("zcode-cli variant", func(t *testing.T) {
		// When ZCode is launched as a sub-process, the prompt sometimes
		// drops the "You are" intro but keeps the "ZCode CLI" self-tag.
		// The registry pattern "zcode cli" must still match.
		prompt := "Sub-agent for ZCode CLI workflow automation."
		if got := DetectAgentFromSystemPrompt(prompt); got != "zcode" {
			t.Fatalf("DetectAgentFromSystemPrompt = %q, want %q", got, "zcode")
		}
	})
}

// TestAgentSignatureOpencode verifies OpenCode CLI detection. OpenCode's
// User-Agent is "OpenCode/{version}" and its system prompt begins with
// "You are opencode, an interactive CLI tool that helps users with
// software engineering tasks."
func TestAgentSignatureOpencode(t *testing.T) {
	ResetAgentPatterns()
	t.Cleanup(ResetAgentPatterns)

	t.Run("header path", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("User-Agent", "OpenCode/0.5.0 (linux; x86_64)")
		if got := ExtractAgentName(req); got != "opencode" {
			t.Fatalf("ExtractAgentName = %q, want %q", got, "opencode")
		}
	})

	t.Run("system prompt path", func(t *testing.T) {
		prompt := "You are opencode, an interactive CLI tool that helps users with software engineering tasks. "
		if got := DetectAgentFromSystemPrompt(prompt); got != "opencode" {
			t.Fatalf("DetectAgentFromSystemPrompt = %q, want %q", got, "opencode")
		}
	})

	t.Run("mixed-case variant", func(t *testing.T) {
		// DetectAgentFromSystemPrompt must lowercase the prompt before
		// matching so "OpenCode" / "OPENCODE" all resolve to "opencode".
		prompt := "You are OPENCODE — executing the user's request."
		if got := DetectAgentFromSystemPrompt(prompt); got != "opencode" {
			t.Fatalf("DetectAgentFromSystemPrompt = %q, want %q", got, "opencode")
		}
	})
}

// TestAgentSignaturePrioritisesMostSpecificPattern guards the
// order-sensitivity of the registry: when a single system prompt
// mentions the canonical self-description of two agents, the most-
// specific earlier-registered pattern wins.
//
// Real traffic only ever carries one agent's system prompt per
// request (the orchestrator is invisible to the gateway). The mixed
// prompts below are synthetic; they verify the *registry's* ordering
// discipline without asserting impossible real-world scenarios.
func TestAgentSignaturePrioritisesMostSpecificPattern(t *testing.T) {
	ResetAgentPatterns()
	t.Cleanup(ResetAgentPatterns)

	cases := []struct {
		name  string
		prompt string
		want  string
	}{
		{
			name:   "ZCode before Claude",
			prompt: "You are ZCode, an interactive coding agent for ZCode CLI. Powered by Claude Code under the hood.",
			want:   "zcode",
		},
		{
			name:   "OpenCode before Claude",
			prompt: "You are opencode, an interactive CLI tool. Internally we call Claude Code for inference.",
			want:   "opencode",
		},
		{
			name:   "Claude-Code before Claude",
			prompt: "You are Claude Code by Anthropic. Powered by plain Claude inference.",
			want:   "claude-code",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectAgentFromSystemPrompt(tc.prompt); got != tc.want {
				t.Fatalf("DetectAgentFromSystemPrompt(%q) = %q, want %q",
					tc.prompt, got, tc.want)
			}
		})
	}
}

// TestExtractAgentNameAllThreeSignatures is a sanity-check table that
// proves all three canonical signatures resolve via the header path. It
// is intentionally redundant with the per-agent tests above so a
// regression in the UA switch statement is caught even if one of the
// individual sub-tests is commented out.
func TestExtractAgentNameAllThreeSignatures(t *testing.T) {
	cases := []struct {
		ua   string
		want string
	}{
		{"Cursor/0.42.5", "cursor"},
		{"cursor/1.0", "cursor"},
		{"ZCode/1.2.3", "zcode"},
		{"OpenCode/0.5.0", "opencode"},
		{"opencode-cli/2.0", "opencode"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.ua, "/", "_"), func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			req.Header.Set("User-Agent", tc.ua)
			if got := ExtractAgentName(req); got != tc.want {
				t.Errorf("ExtractAgentName(%q) = %q, want %q", tc.ua, got, tc.want)
			}
		})
	}
}