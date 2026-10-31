package telemetry

import (
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/clienttype"
)

// TestAgentSignatureDomesticVendorRound locks the 2026-10-29
// domestic-vendor round: the nine CN-vendor coding tools (腾讯 CodeBuddy、
// 字节 Trae、百度 Comate/Zulu、阿里 Qwen Code、月之暗面 Kimi CLI、心流
// iFlow、商汤小浣熊、讯飞 iFlyCode、通义灵码) must resolve to one
// canonical name on BOTH identification paths — the UA token tables and
// the system-prompt 特征词 registry — and each branch must REFUSE its
// alphanumeric lookalike (the "ZCodex contains zcode" hazard class; the
// lookalike column documents the boundary case per client).
func TestAgentSignatureDomesticVendorRound(t *testing.T) {
	ResetAgentPatterns()
	t.Cleanup(ResetAgentPatterns)

	cases := []struct {
		name      string // canonical clienttype value
		ua        string // wire UA form
		prompt    string // system-prompt self-description (特征词)
		lookalike string // UA that must NOT fire this branch
	}{
		{"codebuddy", "CodeBuddy/2.0.1 (darwin; arm64)",
			"You are CodeBuddy Code, Tencent's AI coding agent.",
			"CodeBuddyish/9.0"},
		{"trae", "trae-cli/0.4.2",
			"You are Trae, an AI software engineering agent by ByteDance.",
			"TraeX/1.0"},
		{"comate", "zulu/1.3.0",
			"You are Zulu, the Comate CLI by Baidu.",
			"Zuluism/1.0"},
		{"qwen-code", "qwen-code/0.2.0",
			"You are Qwen Code, Alibaba's open-source coding agent.",
			"qwen-coded/1.0"},
		{"kimi-cli", "kimi-cli/1.8.0",
			"You are Kimi CLI, Moonshot AI's coding agent.",
			"KimiK2/1.0"},
		{"iflow", "iflow-cli/0.5.0",
			"You are iFlow, the AI coding CLI.",
			"iFlowless/2.0"},
		{"raccoon", "Raccoon/2.1.0",
			"代码小浣熊 Raccoon 智能编程助手。You are Raccoon.",
			"Raccoonfish/1.0"},
		{"iflycode", "iflycode/1.0.0",
			"iFlyCode 由讯飞星火驱动。You are iFlyCode.",
			"iFlyCodeX/2.0"},
		{"lingma", "lingma/3.0.0",
			"通义灵码 TONGYI Lingma 编程智能体。You are Lingma.",
			"LingmaX/1.0"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			req.Header.Set("User-Agent", tc.ua)
			if got := ExtractAgentName(req); got != tc.name {
				t.Fatalf("UA %q: ExtractAgentName = %q, want %q", tc.ua, got, tc.name)
			}
			if got := DetectAgentFromSystemPrompt(tc.prompt); got != tc.name {
				t.Fatalf("system prompt: DetectAgentFromSystemPrompt = %q, want %q", got, tc.name)
			}
			// The lookalike must not fire THIS branch. It may resolve to
			// another branch or unknown, never to the lookalike's victim.
			req2 := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			req2.Header.Set("User-Agent", tc.lookalike)
			if got := ExtractAgentName(req2); got == tc.name {
				t.Fatalf("lookalike UA %q must not resolve to %q", tc.lookalike, tc.name)
			}
			// The canonical name must survive normalization — a name the
			// allowlist refuses would collapse to "unknown" downstream
			// (sessionmeta, Prometheus labels) even though detection
			// found it.
			if got := clienttype.Normalize(tc.name); got != tc.name {
				t.Fatalf("clienttype.Normalize(%q) = %q, want identity", tc.name, got)
			}
		})
	}
}

// The domestic entries must outrank the bare "you are claude" fallback:
// several of these forks embed Claude prompts, so a prompt that mentions
// both its own product and Claude resolves to the vendor, never to
// "claude".
func TestAgentSignatureDomesticVendorBeforeClaudeFallback(t *testing.T) {
	ResetAgentPatterns()
	t.Cleanup(ResetAgentPatterns)

	// The precedence contract (same shape as the minimax/deepseek test
	// in agent_signature_test.go): the vendor entry beats the BARE
	// "you are claude" fallback when the prompt only mentions Claude as
	// a model. The literal "claude code" bigram legitimately belongs to
	// the earlier claude-code entry and is deliberately not used here.
	prompt := "You are CodeBuddy Code. Some subtask workflows call Claude inference."
	if got := DetectAgentFromSystemPrompt(prompt); got != "codebuddy" {
		t.Fatalf("DetectAgentFromSystemPrompt = %q, want codebuddy (vendor before claude fallback)", got)
	}
	prompt = "You are Kimi CLI for coding; internally some calls go to Claude."
	if got := DetectAgentFromSystemPrompt(prompt); got != "kimi-cli" {
		t.Fatalf("DetectAgentFromSystemPrompt = %q, want kimi-cli", got)
	}
}
