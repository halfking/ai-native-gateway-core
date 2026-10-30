package settings

// spec_routing_plan.go — 2026-10-30 magpie 对比轮（docs/research/
// 2026-10-30-magpie四域对比刷新与优化方案.md §四 P0-1）：候选计划面的
// 热加载旋钮。
//
// 背景：planByTier 的候选总数上限此前是硬编码 12
//（domains/streaming/executors/router.go maxTotal）。大凭据池场景下 12 个
// 候选不够铺开；坏凭据场景想收紧到更少也没有入口。本键把该常量提升为
// 默认值可覆盖的热配置——未配置时行为逐字节不变。
//
// 归类 CategoryRetry：与 survival.attempt_budget 同属「网关重试与超时」
// 聚合组（B5③），两者共同决定一次请求最多触达多少上游。

// KeyRoutingMaxCandidates 覆盖候选计划的总数上限。
// 0 = 未设置（回落内置默认 12）；生效区间 [4, 48]。
const KeyRoutingMaxCandidates = "routing.max_candidates"

// RoutingPlanSpecs returns the candidate-plan hot-reload controls.
func RoutingPlanSpecs() []*Spec {
	return []*Spec{
		{
			Key:             KeyRoutingMaxCandidates,
			EnvName:         EnvNameAuto(KeyRoutingMaxCandidates),
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryRetry,
			Default:         0,
			Min:             floatPtr(4),
			Max:             floatPtr(48),
			Description:     "单次路由计划的候选总数上限（个），0=用内置默认（12）",
			DescriptionLong: "planByTier 产出候选序列后按此上限截断（各层内顺序保持不变，只裁尾）。0 表示未设置、回落内置默认 12；生效区间 [4, 48]。调大让大凭据池铺得更开，调小收紧坏凭据场景的费用放大面。每次 plan 热读取。",
			Unit:            "个",
			DangerLevel:     Dangerous, // 调大直接放大单请求可触达的上游候选数
			HotReload:       true,
		},
	}
}

// RoutingMaxCandidates 读取候选总数上限覆盖值。
// 返回 0 表示未设置（调用方回落内置默认 12）；正值已被 clamp 到 [4, 48]。
func RoutingMaxCandidates() int {
	v := GetPlatformInt(KeyRoutingMaxCandidates, 0)
	if v <= 0 {
		return 0
	}
	return clampThresholdInt(v, 4, 48)
}
