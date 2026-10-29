package clienttype

import "testing"

func TestNormalize(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want string
	}{
		{" Cursor ", "cursor"},
		{"CLAUDE-CODE", "claude-code"},
		{"my-custom-agent", Unknown},
		{"cursor|other", Unknown},
		{"", Unknown},
		{"   ", Unknown},
		// 2026-09-21 audit: domestic coding-agent clients (canonical spellings).
		{"MiniMax-Code", "minimax-code"},
		{"MINIMAX-CODE", "minimax-code"},
		{"DeepSeek-Code", "deepseek-code"},
		{"deepseek-code", "deepseek-code"},
		// 2026-10-29 identification round: pi coding agent (pi.dev)
		{"pi", "pi"},
		{"Pi", "pi"},
	} {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestNormalize_DomesticClientsAcceptAllSpellings pins the canonical
// spellings accepted for the two domestic coding-agent clients added in
// the 2026-09-21 audit. Normalize is case-insensitive but strict on
// separators: "minimax_code" (snake_case) and "deepseek-code-cli"
// (suffixed) are NOT canonical and must NOT silently expand the
// Prometheus label cardinality.
//
// 2026-09-21 audit.
func TestNormalize_DomesticClientsAcceptAllSpellings(t *testing.T) {
	cases := map[string]string{
		// Canonical (lowercased)
		"minimax-code":  "minimax-code",
		"deepseek-code": "deepseek-code",
		// Mixed case → lowercased canonical
		"MiniMax-Code":  "minimax-code",
		"DeepSeek-Code": "deepseek-code",
		"MiniMax-CODE":  "minimax-code",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}

	// Anti-cases: these must NOT silently expand cardinality.
	antiCases := []string{
		"minimax_code",      // snake_case → unknown
		"deepseek_code",     // snake_case → unknown
		"deepseek-code-cli", // suffixed → unknown
		"minimax-code-pro",  // suffixed → unknown
	}
	for _, in := range antiCases {
		if got := Normalize(in); got != Unknown {
			t.Errorf("Normalize(%q) = %q, want %q (silent cardinality expansion!)", in, got, Unknown)
		}
	}
}

// TestHasToken — 2026-10-29 identification round：双边界 token 匹配的唯一
// 实现自证。两张 UA 表（streaming / telemetry）共用它，这里的每个分支都
// 是两张表的行为契约。
func TestHasToken(t *testing.T) {
	cases := []struct {
		name     string
		haystack string
		token    string
		want     bool
	}{
		// 真实线材形态（正臂）
		{"bare MiniMaxCode", "minimaxcode", "minimaxcode", true},
		{"MiniMaxCode with version", "minimaxcode/1.2.0", "minimaxcode", true},
		{"MiniMaxCode with space suffix", "minimaxcode macos arm64", "minimaxcode", true},
		{"claude-cli with version", "claude-cli/2.1.75 (external, cli)", "claude-cli", true},
		{"zcode with ai-sdk tail", "zcode/3.16.3 ai-sdk/provider-utils/4.0.39 runtime/node.js/24", "zcode", true},
		{"token mid-UA after space", "some proxy zcode/1.0", "zcode", true},
		{"hyphenated vendor form", "minimax-code/1.0.0 (macos; arm64)", "minimax-code", true},
		{"deepseek-cli", "deepseek-cli/0.3.1 (darwin; arm64)", "deepseek-cli", true},
		{"codex-cli", "codex-cli/0.1", "codex", true},
		// 2026-10-29 identification round: pi coding agent（pi.dev 官方
		// formatUserAgent 形态，211 实测 2385 条/24h）。裸 "pi" token 的
		// 双边界是本轮最关键的负臂面：普通英文/工具名里 "pi" 太常见。
		{"pi detailed UA", "pi/1.4.0 (linux; node/v22.23.4; x64)", "pi", true},
		{"pi short form", "pi", "pi", true},
		{"pi after product prefix", "proxy pi/1.0", "pi", true},
		// 误报防线（负臂）：字母数字延续必须拒绝。连字符也是边界，所以
		// "raspberry-pi" 会命中裸 "pi" —— 它不是 UA 产品形态（真实 UA 里
		// 不会出现），登记为已知假阳性而非防线；真正的防线是 "pip"/"api"
		// 这类字母数字延续（均拒绝）。
		{"pip must not match", "pip/24.0", "pi", false},
		{"api must not match", "api-client/2.0", "pi", false},
		{"openai-sdk must not match", "openai-python/1.0", "pi", false},
		// 误报防线（负臂）
		{"MiniMaxCoder must not match", "minimaxcoder/1.0", "minimaxcode", false},
		{"ZCodex must not match zcode", "zcodex/2.0", "zcode", false},
		{"ZCodex must not match codex (leading)", "zcodex/2.0", "codex", false},
		{"visualized must not match zed (leading)", "visualized/2.0", "zed", false},
		{"claude-clix must not match", "claude-clix/1.0", "claude-cli", false},
		{"jython must not match python", "jython/2.7", "python", false},
		{"hcurl must not match curl", "hcurl/1.0", "curl", false},
		// 边界形状：连字符与斜杠是边界
		{"prefix-hyphen ok", "x-minimax-code/1.0", "minimax-code", true},
		{"prefix-slash ok", "proxy/minimaxcode", "minimaxcode", true},
		{"empty haystack", "", "zcode", false},
		{"empty token", "zcode/1.0", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasToken(tc.haystack, tc.token); got != tc.want {
				t.Errorf("HasToken(%q, %q) = %v, want %v", tc.haystack, tc.token, got, tc.want)
			}
		})
	}
}
