package nativepair

import "testing"

func TestMatch_BuiltinPairs(t *testing.T) {
	cases := []struct {
		client string
		model  string
		want   bool
	}{
		// 三条内置原生对（正臂）
		{"zcode", "glm-5.3", true},
		{"zcode", "glm-4.6", true},
		{"zcode", "z-ai/glm-5.2", true}, // 宿主前缀容忍
		{"minimax-code", "minimax-m2", true},
		{"minimax-code", "MiniMax-M2.7", true}, // 大小写不敏感
		{"deepseek-code", "deepseek-chat", true},
		{"deepseek-code", "deepseek-reasoner", true},
		// RISK-6（2026-10-29）：第四条内置原生对 Claude Code↔Claude
		{"claude-code", "claude-sonnet-5", true},
		{"claude-code", "claude-opus-4.5", true},
		{"claude-code", "Claude-Haiku-4", true},            // 大小写不敏感
		{"claude-code", "anthropic/claude-sonnet-5", true}, // 宿主前缀容忍
		// 同名前缀但非「前缀-」段 —— 不得误判
		{"zcode", "glmm-x", false},
		{"zcode", "glmxx", false},
		// 原生客户端打到别厂模型 —— 不是原生对
		{"zcode", "deepseek-chat", false},
		{"minimax-code", "glm-5.3", false},
		{"deepseek-code", "kimi-k2", false},
		{"claude-code", "minimax-m2", false},
		{"claude-code", "glm-5.3", false},
		// 非原生客户端打到原生模型 —— 也不是
		{"cursor", "glm-5.3", false},
		{"unknown", "glm-5.3", false},
		{"", "glm-5.3", false},
		// 空模型
		{"zcode", "", false},
		{"zcode", "  ", false},
		{"zcode", "vendor/", false},
	}
	for _, tc := range cases {
		if got := Match(tc.client, tc.model); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.client, tc.model, got, tc.want)
		}
	}
}
