package nativepair

import "testing"

func TestMatch_BuiltinPairs(t *testing.T) {
	cases := []struct {
		client  string
		catalog string
		model   string
		want    bool
	}{
		// 内置原生对（正臂）——模型族对上且宿主是原厂 catalog
		{"zcode", "zhipu", "glm-5.3", true},
		{"zcode", "zhipu", "glm-4.6", true},
		{"zcode", "zhipu", "z-ai/glm-5.2", true}, // 宿主前缀容忍
		{"zcode", "bigmodel", "glm-5.3", true},   // 原厂别名
		{"minimax-code", "minimax", "minimax-m2", true},
		{"minimax-code", "minimax", "MiniMax-M2.7", true}, // 大小写不敏感
		{"deepseek-code", "deepseek", "deepseek-chat", true},
		{"deepseek-code", "deepseek", "deepseek-reasoner", true},
		// RISK-6（2026-10-29）：第四条内置原生对 Claude Code↔Claude
		{"claude-code", "anthropic", "claude-sonnet-5", true},
		{"claude-code", "anthropic", "claude-opus-4.5", true},
		{"claude-code", "anthropic", "Claude-Haiku-4", true},            // 大小写不敏感
		{"claude-code", "anthropic", "anthropic/claude-sonnet-5", true}, // 宿主前缀容忍

		// ── 2026-10-29 厂商感知闸门（nvidia 直通事故轮）──────────────────
		// 事故原样：zcode → nvidia 托管的 z-ai/glm-5.3-flash。模型族对上，
		// 但宿主不是原厂 ⇒ 不直通（否则 reasoning/enable_thinking 每请求
		// 两轮 400，且学习式预剥离被豁免永不收敛）。
		{"zcode", "nvidia", "z-ai/glm-5.3-flash", false},
		{"zcode", "NVIDIA", "z-ai/glm-5.3-flash", false}, // catalog 大小写不敏感
		// 其它已知第三方宿主同样不直通
		{"zcode", "openrouter", "glm-5.3", false},
		{"zcode", "siliconflow", "glm-5.3", false},
		{"minimax-code", "nvidia", "minimaxai/minimax-m2.7", false},
		{"deepseek-code", "nvidia", "deepseek-ai/deepseek-r1", false},
		{"claude-code", "apiclaude", "claude-sonnet-5", false}, // 转售商非原厂
		// 空 catalog（未挂 catalog 的中继）—— 按第三方宿主处理
		{"zcode", "", "glm-5.3", false},
		{"zcode", "  ", "glm-5.3", false},

		// 同名前缀但非「前缀-」段 —— 不得误判
		{"zcode", "zhipu", "glmm-x", false},
		{"zcode", "zhipu", "glmxx", false},
		// 原生客户端打到别厂模型 —— 不是原生对
		{"zcode", "zhipu", "deepseek-chat", false},
		{"minimax-code", "minimax", "glm-5.3", false},
		{"deepseek-code", "deepseek", "kimi-k2", false},
		{"claude-code", "anthropic", "minimax-m2", false},
		{"claude-code", "anthropic", "glm-5.3", false},
		// 非原生客户端打到原生模型 —— 也不是
		{"cursor", "zhipu", "glm-5.3", false},
		{"unknown", "zhipu", "glm-5.3", false},
		{"", "zhipu", "glm-5.3", false},
		// 空模型
		{"zcode", "zhipu", "", false},
		{"zcode", "zhipu", "  ", false},
		{"zcode", "zhipu", "vendor/", false},
	}
	for _, tc := range cases {
		if got := Match(tc.client, tc.catalog, tc.model); got != tc.want {
			t.Errorf("Match(%q, %q, %q) = %v, want %v", tc.client, tc.catalog, tc.model, got, tc.want)
		}
	}
}
