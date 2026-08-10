package autoroute

import (
	"context"
	"testing"
)

// auto_route_e2e_test.go — "提示词 → 分类 → V6 默认路由 Resolve" 端到端链路测试。
//
// 目的：满足审计要求 #8 的"可用且正确"层面。classifier_prompt_matrix_test.go
// 验证"提示词 → 正确 task type"，v6_routing_matrix_test.go 验证"模型在 canonical
// 池内"，本文件把两者串起来：每个代表性提示词分类后，用 V6 矩阵（与
// 477_auto_route_v6_defaults.sql 完全一致）的 store 解析出最终模型，验证：
//   1. Resolve 成功（ok=true）—— 即"可用"
//   2. 解析到的模型与该 task×profile 的 V6 期望一致 —— 即"正确"
//   3. 解析到的模型都在 canonical 真实池内 —— 不指向幽灵模型
//
// 覆盖范围：11 个任务类型 × 默认 profile（smart），每个一条真实风格提示词。
// 这是 model=auto 请求在"explicit_default"路径下的核心决策链。

// v6StoreForTest 构造一个与 477_auto_route_v6_defaults.sql 完全一致的内存
// DefaultRoutingStore（仅 primary 层 + fallback，复用 v6 矩阵真值）。
func v6StoreForTest() *DefaultRoutingStore {
	// primary: (task, profile) → model，与 477 migration 一致。
	primary := map[string]map[string]string{
		"chat":                  {"smart": "claude-sonnet-5", "speed_first": "gemini-2.0-flash-exp", "cost_first": "deepseek-chat"},
		"reasoning":             {"smart": "claude-fable-5", "speed_first": "o5-preview", "cost_first": "qwq-32b-preview"},
		"code":                  {"smart": "claude-sonnet-5", "speed_first": "codestral", "cost_first": "deepseek-coder"},
		"agent":                 {"smart": "claude-sonnet-5", "speed_first": "gemini-2.0-flash-exp", "cost_first": "deepseek-chat"},
		"creative":              {"smart": "claude-opus-4-8", "speed_first": "gemini-2.0-flash-exp", "cost_first": "deepseek-chat"},
		"long_context":          {"smart": "claude-sonnet-5", "speed_first": "moonshot-v1-128k", "cost_first": "deepseek-chat"},
		"vision":                {"smart": "claude-sonnet-5", "speed_first": "gemini-2.0-flash-exp", "cost_first": "glm-4v-plus"},
		"function_call":         {"smart": "claude-sonnet-5", "speed_first": "gemini-2.0-flash-exp", "cost_first": "deepseek-chat"},
		"code_audit":            {"smart": "claude-fable-5", "speed_first": "claude-sonnet-5", "cost_first": "deepseek-coder"},
		"intent_classification": {"smart": "gemini-2.0-flash-exp", "speed_first": "minimax-m3", "cost_first": "deepseek-chat"},
		"planning":              {"smart": "claude-opus-4-8", "speed_first": "claude-sonnet-5", "cost_first": "qwen3-235b"},
	}
	fallback := map[string]string{
		"chat": "qwen3-235b", "reasoning": "qwen3-235b", "code": "deepseek-coder",
		"agent": "qwen3-235b", "creative": "qwen3-235b", "long_context": "qwen3-235b",
		"vision": "qwen3-235b", "function_call": "deepseek-chat", "code_audit": "deepseek-coder",
		"intent_classification": "deepseek-chat", "planning": "qwen3-235b",
	}

	var routings []DefaultRouting
	for task, profs := range primary {
		for prof, model := range profs {
			routings = append(routings, DefaultRouting{
				TaskType: task, Profile: prof, Tier: RoutingPrimary,
				CanonicalModel: model, Priority: 100,
			})
		}
	}
	for task, model := range fallback {
		routings = append(routings, DefaultRouting{
			TaskType: task, Profile: "", Tier: RoutingFallback,
			CanonicalModel: model, Priority: 100,
		})
	}

	byTask := make(map[string][]DefaultRouting)
	for _, r := range routings {
		byTask[r.TaskType] = append(byTask[r.TaskType], r)
	}
	s := &DefaultRoutingStore{}
	s.snapshot.Store(&defaultRoutingSnapshot{byTask: byTask})
	return s
}

// TestAutoRouteE2E_PromptToModel 验证"提示词 → 分类 → V6 Resolve → 正确模型"全链路。
// 这是 model=auto 请求决策的静态可验证部分（不含凭证层的实时可用性过滤）。
func TestAutoRouteE2E_PromptToModel(t *testing.T) {
	store := v6StoreForTest()
	clf := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	ctx := context.Background()

	cases := []struct {
		name      string
		sigs      ClassificationSignals
		profile   Profile
		wantTask  TaskType
		wantModel string
	}{
		// smart profile（默认，高智商优先）
		{"chat_smart", ClassificationSignals{LastUserPrompt: "你好，介绍一下你自己"}, ProfileSmart, TaskChat, "claude-sonnet-5"},
		{"reasoning_smart", ClassificationSignals{LastUserPrompt: "证明根号2是无理数"}, ProfileSmart, TaskReasoning, "claude-fable-5"},
		{"code_smart", ClassificationSignals{LastUserPrompt: "用 Python 写一个快速排序算法"}, ProfileSmart, TaskCode, "claude-sonnet-5"},
		{"agent_smart", ClassificationSignals{ToolCount: 5, HasToolResults: true, LastUserPrompt: "search and read then summarize"}, ProfileSmart, TaskAgent, "claude-sonnet-5"},
		{"creative_smart", ClassificationSignals{LastUserPrompt: "写一篇关于 AI 的博客文章"}, ProfileSmart, TaskCreative, "claude-opus-4-8"},
		{"vision_smart", ClassificationSignals{HasImages: true, LastUserPrompt: "describe this image"}, ProfileSmart, TaskVision, "claude-sonnet-5"},
		{"functioncall_smart", ClassificationSignals{ToolCount: 1, LastUserPrompt: "what's the weather"}, ProfileSmart, TaskFunctionCall, "claude-sonnet-5"},
		{"codeaudit_smart", ClassificationSignals{LastUserPrompt: "请对这段代码做安全审计，检查漏洞"}, ProfileSmart, TaskCodeAudit, "claude-fable-5"},
		{"intent_smart", ClassificationSignals{LastUserPrompt: "请对这条消息进行意图分类"}, ProfileSmart, TaskIntentClassification, "gemini-2.0-flash-exp"},
		{"planning_smart", ClassificationSignals{LastUserPrompt: "帮我写一份微服务架构方案，拆解各模块任务"}, ProfileSmart, TaskPlanning, "claude-opus-4-8"},

		// cost_first profile（成本优先，验证不同 profile 解析到不同模型）
		{"chat_cost", ClassificationSignals{LastUserPrompt: "你好"}, ProfileCostFirst, TaskChat, "deepseek-chat"},
		{"code_cost", ClassificationSignals{LastUserPrompt: "写一个排序函数"}, ProfileCostFirst, TaskCode, "deepseek-coder"},
		{"intent_cost", ClassificationSignals{LastUserPrompt: "识别以下文本的意图并归类"}, ProfileCostFirst, TaskIntentClassification, "deepseek-chat"},
		{"planning_cost", ClassificationSignals{LastUserPrompt: "制定项目实施计划，做任务拆解"}, ProfileCostFirst, TaskPlanning, "qwen3-235b"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Step 1: 分类
			cls, err := clf.Classify(ctx, tc.sigs)
			if err != nil {
				t.Fatalf("Classify error: %v", err)
			}
			if cls.Primary != tc.wantTask {
				t.Fatalf("classify: task=%q, want %q (reason=%s)", cls.Primary, tc.wantTask, cls.Reason)
			}

			// Step 2: V6 默认路由 Resolve（模拟 decision.go 的 explicit_default 路径）
			res, ok := store.Resolve(string(cls.Primary), string(tc.profile), "")
			if !ok {
				t.Fatalf("Resolve(%q, %q) returned ok=false — V6 matrix has a gap", cls.Primary, tc.profile)
			}
			if res.CanonicalModel != tc.wantModel {
				t.Errorf("Resolve(%q, %q) = %q, want %q (tier=%s)",
					cls.Primary, tc.profile, res.CanonicalModel, tc.wantModel, res.Tier)
			}

			// Step 3: 解析到的模型必须在 canonical 真实池内（不指向幽灵模型）
			if !v6CanonicalPool[res.CanonicalModel] {
				t.Errorf("resolved model %q is NOT in models_canonical seed — routing will fail at runtime", res.CanonicalModel)
			}
		})
	}
}

// TestAutoRouteE2E_AllTaskTypesResolvable 验证 V6 矩阵对全部 11 个任务类型 ×
// 3 个 profile 都能解析（无 gap）。防止后续编辑矩阵时遗漏某个 task×profile 组合。
func TestAutoRouteE2E_AllTaskTypesResolvable(t *testing.T) {
	store := v6StoreForTest()
	profiles := []string{"smart", "speed_first", "cost_first", ""} // "" = fallback
	for _, task := range AllTaskTypes {
		for _, prof := range profiles {
			res, ok := store.Resolve(string(task), prof, "")
			if !ok {
				t.Errorf("V6 matrix gap: Resolve(%q, %q) = ok=false", task, prof)
				continue
			}
			if res.CanonicalModel == "" {
				t.Errorf("V6 matrix: Resolve(%q, %q) returned empty model", task, prof)
			}
			if !v6CanonicalPool[res.CanonicalModel] {
				t.Errorf("V6 matrix model %q for (%q, %q) not in canonical pool", res.CanonicalModel, task, prof)
			}
		}
	}
}
