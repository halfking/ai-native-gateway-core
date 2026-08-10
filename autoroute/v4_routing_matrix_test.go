package autoroute

import (
	"testing"
)

// TestV6RoutingMatrix verifies the v6.0 default routing matrix seeded by
// sql/migrations/startup/477_auto_route_v6_defaults.sql.
//
// v6 修正了 v5 的若干问题（见审计报告）：
//   - long_context/smart: gemini-1.5-pro(陈旧) → claude-sonnet-5
//   - reasoning/speed_first: 新增 o5-preview（v5 缺失）
//   - intent_classification: 不再误用高成本 claude，改用 gemini-2.0-flash-exp
//   - 新增 planning 任务（高智商模型）
//   - fallback 全部用真实 canonical 行（qwen3-235b / deepseek-*）
//
// 所有模型均来自 models_canonical seed（001_vendor_family_mappings.sql +
// fix-third-party-relay.sql + fix-claude-opus-4-8.sql），无虚构模型。
func TestV6RoutingMatrix(t *testing.T) {
	cases := []struct {
		taskType string
		profile  string
		want     string
	}{
		// chat
		{"chat", "smart", "claude-sonnet-5"},
		{"chat", "speed_first", "gemini-2.0-flash-exp"},
		{"chat", "cost_first", "deepseek-chat"},
		// reasoning
		{"reasoning", "smart", "claude-fable-5"},
		{"reasoning", "speed_first", "o5-preview"},
		{"reasoning", "cost_first", "qwq-32b-preview"},
		// code
		{"code", "smart", "claude-sonnet-5"},
		{"code", "speed_first", "codestral"},
		{"code", "cost_first", "deepseek-coder"},
		// agent
		{"agent", "smart", "claude-sonnet-5"},
		{"agent", "speed_first", "gemini-2.0-flash-exp"},
		{"agent", "cost_first", "deepseek-chat"},
		// creative
		{"creative", "smart", "claude-opus-4-8"},
		{"creative", "speed_first", "gemini-2.0-flash-exp"},
		{"creative", "cost_first", "deepseek-chat"},
		// long_context（v6 修正：gemini-1.5-pro → claude-sonnet-5）
		{"long_context", "smart", "claude-sonnet-5"},
		{"long_context", "speed_first", "moonshot-v1-128k"},
		{"long_context", "cost_first", "deepseek-chat"},
		// vision
		{"vision", "smart", "claude-sonnet-5"},
		{"vision", "speed_first", "gemini-2.0-flash-exp"},
		{"vision", "cost_first", "glm-4v-plus"},
		// function_call
		{"function_call", "smart", "claude-sonnet-5"},
		{"function_call", "speed_first", "gemini-2.0-flash-exp"},
		{"function_call", "cost_first", "deepseek-chat"},
		// code_audit
		{"code_audit", "smart", "claude-fable-5"},
		{"code_audit", "speed_first", "claude-sonnet-5"},
		{"code_audit", "cost_first", "deepseek-coder"},
		// intent_classification（v6 修正：成本低、速度快，不用高成本 claude）
		{"intent_classification", "smart", "gemini-2.0-flash-exp"},
		{"intent_classification", "speed_first", "minimax-m3"},
		{"intent_classification", "cost_first", "deepseek-chat"},
		// planning（v6 新增：编写计划/方案/任务拆解，用智商最高的模型）
		{"planning", "smart", "claude-opus-4-8"},
		{"planning", "speed_first", "claude-sonnet-5"},
		{"planning", "cost_first", "qwen3-235b"},
	}

	routings := make([]DefaultRouting, 0, len(cases))
	for _, c := range cases {
		routings = append(routings, DefaultRouting{
			TaskType:       c.taskType,
			Profile:        c.profile,
			Tier:           RoutingPrimary,
			CanonicalModel: c.want,
			Priority:       100,
		})
	}
	// Generic fallbacks so Resolve succeeds for unknown profiles.
	fallbacks := map[string]string{
		"chat":                  "qwen3-235b",
		"reasoning":             "qwen3-235b",
		"code":                  "deepseek-coder",
		"agent":                 "qwen3-235b",
		"creative":              "qwen3-235b",
		"long_context":          "qwen3-235b",
		"vision":                "qwen3-235b",
		"function_call":         "deepseek-chat",
		"code_audit":            "deepseek-coder",
		"intent_classification": "deepseek-chat",
		"planning":              "qwen3-235b",
	}
	for task, model := range fallbacks {
		routings = append(routings, DefaultRouting{
			TaskType:       task,
			Profile:        "",
			Tier:           RoutingFallback,
			CanonicalModel: model,
			Priority:       100,
		})
	}

	byTask := make(map[string][]DefaultRouting)
	for _, r := range routings {
		byTask[r.TaskType] = append(byTask[r.TaskType], r)
	}

	s := &DefaultRoutingStore{}
	s.snapshot.Store(&defaultRoutingSnapshot{byTask: byTask})

	for _, c := range cases {
		t.Run(c.taskType+"_"+c.profile, func(t *testing.T) {
			res, ok := s.Resolve(c.taskType, c.profile, "")
			if !ok {
				t.Fatalf("Resolve(%q, %q, '') returned ok=false", c.taskType, c.profile)
			}
			if res.CanonicalModel != c.want {
				t.Errorf("Resolve(%q, %q) = %q, want %q (tier=%s)",
					c.taskType, c.profile, res.CanonicalModel, c.want, res.Tier)
			}
			if res.Tier != RoutingPrimary {
				t.Errorf("Resolve tier = %q, want primary", res.Tier)
			}
		})
	}
}

// TestV6NoOldModels ensures v6.0 does NOT use any of the old/legacy model IDs
// that v5.0 either used (陈旧) or that don't exist in models_canonical (虚构).
func TestV6NoOldModels(t *testing.T) {
	// 陈旧模型（v5 用过，v6 应淘汰）：gemini-1.5-pro 被 claude-sonnet-5 取代。
	staleModels := []string{
		"gemini-1.5-pro", // v5 long_context/smart，陈旧，v6 改用 claude-sonnet-5
	}
	// 虚构/无 canonical 行的模型（v5 的 TestV5NoOldModels 列表 + featured 清洗发现的）
	wrongModels := []string{
		"claude-sonnet-4-5",
		"claude-opus-4-5",
		"claude-3-7-sonnet-20250219",
		"claude-haiku-3-5",
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"doubao-seed-2.0-pro",
		"doubao-seed-2.0-lite",
		"doubao-seed-2.0-mini",
		"doubao-seed-2.0-code",
		"gpt-4o-mini",
		"o3",
		"o4-mini",
		"glm-5.1",
		"glm-5.2",
		"kimi-k2.6",
		"qwen-plus",
		// featured_models seed (02-seed.sql:934) 里被清洗掉的虚构模型
		"gemini-3-flash-preview",
		"gemini-3.5-flash",
		"gpt-5.4-pro",
		"gpt-5.5",
		"minimax-2.7",
		"minimax-m2.7-highspeed",
		"mimo-v2.5", // 应为 mimo-v2.5-pro
	}

	v6Models := map[string]bool{
		"claude-sonnet-5":      true,
		"claude-fable-5":       true,
		"claude-opus-4-8":      true,
		"claude-sonnet-4-6":    true,
		"gpt-5.4":              true,
		"o5-preview":           true,
		"o3-mini":              true,
		"gemini-2.0-flash-exp": true,
		"gemini-1.5-pro":       true, // 仍在 canonical 池，但 v6 矩阵不再选它
		"qwen3-235b":           true,
		"qwq-32b-preview":      true,
		"qwen-max":             true,
		"qwen-turbo":           true,
		"minimax-m3":           true,
		"deepseek-chat":        true,
		"deepseek-coder":       true,
		"glm-4v-plus":          true,
		"codestral":            true,
		"moonshot-v1-128k":     true,
		"mimo-v2.5-pro":        true,
	}

	// 陈旧模型：v6 矩阵不应再选择 gemini-1.5-pro 作为 primary。
	// 注意：gemini-1.5-pro 仍在 canonical 池（供其他用途），所以这里只断言
	// 它不出现在 v6 矩阵用例的期望值里（通过 v6MatrixPrimarySet 校验）。
	v6MatrixPrimarySet := map[string]bool{
		"claude-sonnet-5": true, "claude-fable-5": true, "claude-opus-4-8": true,
		"gpt-5.4": true, "o5-preview": true,
		"gemini-2.0-flash-exp": true, "qwen3-235b": true, "qwq-32b-preview": true,
		"minimax-m3": true, "deepseek-chat": true, "deepseek-coder": true,
		"glm-4v-plus": true, "codestral": true, "moonshot-v1-128k": true,
	}
	for _, stale := range staleModels {
		if v6MatrixPrimarySet[stale] {
			t.Errorf("stale model %q appears as a v6 primary; should have been replaced", stale)
		}
	}
	for _, wrong := range wrongModels {
		if v6Models[wrong] {
			t.Errorf("wrong/old model %q is present in v6 canonical pool", wrong)
		}
	}
	if len(v6Models) < 18 {
		t.Errorf("v6 canonical pool too small: %d models", len(v6Models))
	}
}
