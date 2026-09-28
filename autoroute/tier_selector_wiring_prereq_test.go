package autoroute

import (
	"context"
	"testing"
)

// R77 启动评估（auto P2 TierSelector 接线）：把「TierSelector 是 V3 专属组件」这条
// 前置依赖钉成机器可校验的事实。
//
// 背景：TierSelector 自 R43 起被登记为「写了没人读」的空壳闭环，R65/R66/R68/R69
// 连续五轮挂账为 owner 拍板项，R66 点名的优先入口是「接线启动评估」。本文件就是那份
// 评估的可执行部分——它不接线，只钉住「在什么条件下接线才有意义」。
//
// 评估结论（2026-09-28，HEAD 94db2f871）：
//
//	全仓存在两套互不相交的 TaskType 词表。
//
//	V2 词表（classifier.go + task_types_ext.go）——生产分类器
//	    NewHeuristicClassifierWithTuning（cmd/gateway/main.go:5036）实际发出的：
//	    chat / reasoning / code / agent / creative / long_context / vision /
//	    function_call / code_audit / intent_classification / planning
//	    240 例 E2E 实测发出的正是这 11 类。
//
//	V3 词表（task_types_v3.go，AllTaskTypesV3）——实验对照用：
//	    architecture / audit / debugging / coding / refactoring / testing /
//	    devops / documentation / summary / dependency
//	    恰好等于 task_type_tier_config 的 10 条种子行。
//
//	而 NewV3Classifier 与 NewTierSelector **都零生产构造点**（NewV3Classifier 自身
//	注释还写着「保留供实验对照, 下轮清理候选」）。
//
//	因此把 TierSelector 接进生产热路径，今天拿不到任何按任务类型的真实配置：
//	  * enableV3=false → SelectTier 第一行就返回 tier-b，对**所有**请求一视同仁，
//	    是一道会把 tier-a / tier-c 候选全部硬过滤掉的闸（离线套件实测分布
//	    tier-a=85 / tier-b=105 / tier-c=50，爆炸半径远超「小步灰度」）；
//	  * enableV3=true  → 分类器仍发 V2 类型，配置查询全部 miss，
//	    落到 getDefaultTier → TaskTypeTierMapping 无 V2 键 → 仍默认 tier-b。
//
//	两条路都是「零收益 + 大爆炸半径」。正确顺序是 V3 分类器先落地（P3，影子观察
//	≥7 天是硬前置），TierSelector 才有真实词表可消费。接线不是独立任务，它是 V3
//	分类器灰度的下游消费者。

// v2RuntimeTaskTypes 是生产分类器（V2 heuristic）实际发出的任务类型全集。
// 取自 classifier.go 与 task_types_ext.go 的常量字面量——刻意用字面量而不是
// 常量表达式，这样将来有人改常量值时本测试会立刻红，而不是悄悄跟着变。
var v2RuntimeTaskTypes = []string{
	"chat", "reasoning", "code", "agent", "creative", "long_context",
	"vision", "function_call", "code_audit", "intent_classification", "planning",
}

// TestTierSelectorConfigVocabularyIsV3Only 钉住：tier 配置表的词表 == V3 词表。
// 若将来有人给 task_type_tier_config 补 V2 类型的行（例如让 V2 流量也能按任务类型
// 拿配置），这条会红——那时 TierSelector 的前置条件已经改变，评估结论需要重做。
func TestTierSelectorConfigVocabularyIsV3Only(t *testing.T) {
	// 真库种子（V370 形态，db.ensureTaskTypeTierConfig 写入）与 AllTaskTypesV3
	// 一一对应。这是「tier 配置是为 V3 分类器准备的」的直接证据。
	tierConfigVocabulary := []string{
		"architecture", "audit", "debugging", "coding", "refactoring",
		"testing", "devops", "documentation", "summary", "dependency",
	}

	if len(AllTaskTypesV3) != len(tierConfigVocabulary) {
		t.Fatalf("AllTaskTypesV3 has %d entries but the tier config seeds %d; "+
			"the two are maintained as the same vocabulary — re-check the seeding",
			len(AllTaskTypesV3), len(tierConfigVocabulary))
	}
	for i, want := range tierConfigVocabulary {
		if got := string(AllTaskTypesV3[i]); got != want {
			t.Errorf("AllTaskTypesV3[%d] = %q, want %q (order/composition changed; "+
				"task_type_tier_config seeds these values verbatim)", i, got, want)
		}
	}

	// 默认分层映射与置信度阈值必须覆盖同一套 V3 词表——否则「配置 miss 时兜底」
	// 这条路径在 V3 词表上也会落空。
	for _, tt := range AllTaskTypesV3 {
		if _, ok := TaskTypeTierMapping[tt]; !ok {
			t.Errorf("TaskTypeTierMapping missing V3 type %q — getDefaultTier would "+
				"fall through to tier-b for a task type that has a config row", tt)
		}
		if _, ok := MinConfidenceThresholds[tt]; !ok {
			t.Errorf("MinConfidenceThresholds missing V3 type %q", tt)
		}
	}
}

// TestTierConfigVocabularyDisjointFromRuntimeVocabulary 是本文件的核心钉桩：
// V2 生产词表与 V3 tier 配置词表**零交集**。这解释了为什么现在接线没有收益。
//
// 一旦将来 V3 分类器在生产启用（或有人给 tier 配置补 V2 行），这条会红——
// 那正是「TierSelector 接线启动评估需要重做」的信号。
func TestTierConfigVocabularyDisjointFromRuntimeVocabulary(t *testing.T) {
	v3 := make(map[string]struct{}, len(AllTaskTypesV3))
	for _, tt := range AllTaskTypesV3 {
		v3[string(tt)] = struct{}{}
	}
	for _, v2 := range v2RuntimeTaskTypes {
		if _, shared := v3[v2]; shared {
			t.Errorf("V2 runtime type %q now also appears in AllTaskTypesV3; the two "+
				"vocabularies were disjoint, which is why TierSelector wiring had zero "+
				"benefit while the V2 classifier is the production one", v2)
		}
	}
}

// TestSelectTierV3DisabledIsBlanketTierB 钉住爆炸半径：enableV3=false 时
// SelectTier 对**任何**输入都返回 tier-b。这是一个「一刀切硬过滤」的形状，
// 不是「按任务类型选层」。接线时若把它放进生效路径而没有先做影子观测，
// 会把非 tier-b 的候选全滤掉。
func TestSelectTierV3DisabledIsBlanketTierB(t *testing.T) {
	ts := NewTierSelector(nil, false)
	inputs := []TierSelectionInput{
		{TaskType: TaskCode, Confidence: 0.95},
		{TaskType: TaskCodeAudit, Confidence: 0.85},
		{TaskType: TaskPlanning, Confidence: 0.82},
		{TaskType: TaskReasoning, Confidence: 0.99},
		{TaskType: TaskVision, Confidence: 0.90},
	}
	for _, in := range inputs {
		got, err := ts.SelectTier(context.Background(), in)
		if err != nil {
			t.Fatalf("SelectTier(%s) error: %v", in.TaskType, err)
		}
		if got.Tier != "tier-b" {
			t.Errorf("SelectTier(%s) with enableV3=false returned tier %q, want tier-b "+
				"(v3-disabled branch is a blanket default, not a per-type selection)",
				in.TaskType, got.Tier)
		}
		if got.ConfigSource != "default" {
			t.Errorf("SelectTier(%s).ConfigSource = %q, want \"default\"", in.TaskType, got.ConfigSource)
		}
	}
}

// TestSelectTierV3EnabledStillDefaultsForV2Types 钉住第二条路：即使打开
// enableV3，只要配置表没有 V2 行、V2 类型也不在 TaskTypeTierMapping 里，
// V2 流量拿到的仍然是兜底默认，而不是它自己的分层意图。
//
// 注意 header 覆盖与深度降级仍然生效——它们在配置查询之前短路返回，
// 与本测试的断言不冲突（这里刻意不传 HeaderTier / AgentDepth）。
func TestSelectTierV3EnabledStillDefaultsForV2Types(t *testing.T) {
	ts := NewTierSelector(nil, true)
	for _, task := range []TaskType{TaskCode, TaskCodeAudit, TaskPlanning, TaskReasoning} {
		// 前提自证：V2 类型确实不在任何 V3 映射里。
		if _, ok := TaskTypeTierMapping[task]; ok {
			t.Fatalf("precondition broken: V2 type %q is now in TaskTypeTierMapping; "+
				"re-run the wiring evaluation", task)
		}
		got, err := ts.SelectTier(context.Background(), TierSelectionInput{
			TaskType: task, Confidence: 0.95,
		})
		if err != nil {
			t.Fatalf("SelectTier(%s) error: %v", task, err)
		}
		if got.ConfigSource != "default" {
			t.Errorf("SelectTier(%s).ConfigSource = %q, want \"default\" — with no "+
				"config row and no V3 mapping the result is the fallback, so wiring "+
				"today carries no per-type intent", task, got.ConfigSource)
		}
	}
}
