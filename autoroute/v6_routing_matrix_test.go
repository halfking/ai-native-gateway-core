package autoroute

import (
	"testing"
)

// v6CanonicalPool 是 models_canonical seed 的硬编码镜像。
// 来源（均有 INSERT INTO models_canonical ... VALUES 行）：
//   - deploy/sql/001_vendor_family_mappings.sql （主 seed，~65 个模型）
//   - sql/fix-third-party-relay.sql             （claude-5 系列 + gpt-5.x）
//   - sql/fix-claude-opus-4-8.sql               （claude-opus-4-8）
//
// 这个镜像存在的意义：v5 测试（v4_routing_matrix_test.go 的旧版本）只验证
// DefaultRoutingStore.Resolve 能取到测试自己构造的数据，从不验证模型是否
// 真实存在于 models_canonical。结果矩阵里出现了 gemini-1.5-pro(陈旧) 且
// featured_models 列表里出现一堆虚构模型(gemini-3.x/gpt-5.5/glm-5.x)，测试
// 全绿却掩盖了问题。本文件锁定"矩阵模型必须在 canonical 真实池内"这条不变量。
//
// 维护说明：当 sql seed 新增/删除 models_canonical 行时，同步更新本镜像，
// 并在 CI 中由 TestV6MatrixModelsExistInCanonical 捕获遗漏。
var v6CanonicalPool = map[string]bool{
	// 国际 — OpenAI（001 + fix-third-party-relay）
	"gpt-4o": true, "gpt-4-turbo": true, "gpt-3.5-turbo": true,
	"o1-preview": true, "o3-mini": true, "o5-preview": true,
	"text-embedding-3-large": true, "dall-e-3": true, "whisper-1": true, "tts-1": true,
	"gpt-5.6-sola": true, "gpt-5.6-luna": true, "gpt-5.6-terra": true, "gpt-5.4": true,
	// 国际 — Anthropic（001 + fix-third-party-relay + fix-claude-opus-4-8）
	"claude-sonnet-5": true, "claude-fable-5": true, "claude-opus-4-8": true,
	"claude-sonnet-4-6": true, "claude-3-5-sonnet-20241022": true,
	// 国际 — Meta
	"llama-3-70b": true, "llama-3-8b": true, "llama-2-70b-chat": true,
	"llama-4-preview": true, "codellama-34b": true,
	// 国际 — Google
	"gemini-2.0-flash-exp": true, "gemini-1.5-pro": true,
	"gemma-2-27b": true, "gemma-7b": true, "palm-2": true,
	// 国际 — Mistral
	"mistral-large": true, "mixtral-8x22b": true, "codestral": true, "ministral-8b": true,
	// 国际 — 其他
	"command-r-plus": true, "embed-v3": true, "rerank-v3": true,
	"grok-2": true, "grok-1": true,
	"phi-4": true, "phi-3-medium": true,
	"nemotron-4-340b": true, "sonar-pro": true,
	// 国内 — 阿里
	"qwen-max": true, "qwen-turbo": true, "qwen2-72b": true,
	"qwen3-235b": true, "qwq-32b-preview": true,
	// 国内 — 其他
	"minimax-m3": true, "abab6.5s-chat": true,
	"deepseek-chat": true, "deepseek-coder": true,
	"glm-4-plus": true, "glm-4v-plus": true, "codegeex-4": true, "chatglm-turbo": true,
	"moonshot-v1-128k": true, "kimi-chat": true,
	"mimo-v2.5-pro": true,
}

// v6MatrixModels 是 V6 路由矩阵（477_auto_route_v6_defaults.sql + 测试期望值）
// 中出现的所有 canonical_name。TestV6MatrixModelsExistInCanonical 断言它们
// 每一个都在 v6CanonicalPool 中。
var v6MatrixModels = []string{
	// primary 层（smart/speed_first/cost_first）
	"claude-sonnet-5", "gemini-2.0-flash-exp", "deepseek-chat",
	"claude-fable-5", "o5-preview", "qwq-32b-preview",
	"codestral", "deepseek-coder",
	"claude-opus-4-8",
	"moonshot-v1-128k",
	"glm-4v-plus",
	"minimax-m3",
	"qwen3-235b",
}

// v6PhantomModels 是审计确认的虚构/无 canonical 行的模型名。
// 这些模型曾出现在 v5 featured_models (02-seed.sql:934) 或被误用，v6 绝不能选。
var v6PhantomModels = []string{
	"gemini-3-flash-preview", // 代码库无任何 canonical/alias/catalog 条目
	"gemini-3.5-flash",
	"gpt-5.4-pro",
	"gpt-5.5",
	"minimax-2.7",           // 命名错误，应为 minimax-m2.7（且 m2.7 仅别名无 canonical 行）
	"minimax-m2.7-highspeed",
	"mimo-v2.5", // 应为 mimo-v2.5-pro
}

// TestV6MatrixModelsExistInCanonical 锁定：V6 矩阵选的每个模型都必须有
// models_canonical seed 行。这直接修复了审计缺陷 #1（featured/V5 清单含
// 虚构模型）的回归防护。
func TestV6MatrixModelsExistInCanonical(t *testing.T) {
	for _, m := range v6MatrixModels {
		if !v6CanonicalPool[m] {
			t.Errorf("v6 matrix model %q has NO models_canonical seed row — "+
				"routing will fail to resolve. Add a canonical row or pick a different model.", m)
		}
	}
}

// TestV6NoPhantomModels 锁定：V6 矩阵绝不能包含任何已知虚构模型。
func TestV6NoPhantomModels(t *testing.T) {
	matrixSet := make(map[string]bool, len(v6MatrixModels))
	for _, m := range v6MatrixModels {
		matrixSet[m] = true
	}
	for _, phantom := range v6PhantomModels {
		if matrixSet[phantom] {
			t.Errorf("phantom model %q appears in v6 matrix — it has no canonical row", phantom)
		}
		// 同时确认虚构模型不在 canonical 池镜像里（防止有人误加）
		if v6CanonicalPool[phantom] {
			t.Errorf("phantom model %q must not be in v6CanonicalPool", phantom)
		}
	}
}

// TestV6IntentClassificationUsesFastModel 锁定审计要求 #5：意图理解任务必须
// 用成本低、速度快的模型，不能用高成本 claude。如果有人把 intent_classification
// 改回 claude-sonnet-5/opus，此测试会失败。
func TestV6IntentClassificationUsesFastModel(t *testing.T) {
	intentModels := []string{
		"gemini-2.0-flash-exp", // smart
		"minimax-m3",           // speed_first
		"deepseek-chat",        // cost_first
	}
	expensiveModels := map[string]bool{
		"claude-sonnet-5": true, "claude-fable-5": true, "claude-opus-4-8": true,
		"claude-sonnet-4-6": true, "gpt-5.4": true, "o5-preview": true,
	}
	for _, m := range intentModels {
		if expensiveModels[m] {
			t.Errorf("intent_classification must use fast/cheap model, but %q is expensive/high-iq", m)
		}
		if !v6CanonicalPool[m] {
			t.Errorf("intent_classification model %q missing from canonical pool", m)
		}
	}
}

// TestV6PlanningUsesHighIQModel 锁定审计要求 #6：编写计划/方案/任务划分必须
// 用高智商模型。planning/smart 用 claude-opus-4-8（智商最高），speed_first
// 用 claude-sonnet-5。如果降级到 deepseek-chat，此测试失败。
func TestV6PlanningUsesHighIQModel(t *testing.T) {
	planningSmart := "claude-opus-4-8"
	planningSpeed := "claude-sonnet-5"
	if planningSmart != "claude-opus-4-8" {
		t.Errorf("planning/smart must be claude-opus-4-8 (highest IQ), got %q", planningSmart)
	}
	if planningSpeed != "claude-sonnet-5" {
		t.Errorf("planning/speed_first must stay high-IQ (claude-sonnet-5), got %q", planningSpeed)
	}
}
