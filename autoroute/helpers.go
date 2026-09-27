package autoroute

// helpers.go — R50 F14（2026-09-21）从 role_llm_router.go 抽出的、与
// RoleLLMRouter 状态无关的 tier-failover 拼接小函数。搬迁理由：
//   - 函数本身只依赖 ScoredCandidate + []string，无 router/snapshot 状态，
//     长期挂在 role_llm_router.go 会误导后续维护者（误以为是 role 路由内部
//     实现而错改 R50 F14 契约）。
//   - V1 decision.go P3 对齐（P1-P5 plan）要复用同一个函数，独立的 helpers.go
//     便于两个调用站点（V1 role/pin promote、V2 role/pin promote）共享文档
//     与单测。
//
// 行为契约（务必与 R50 F14 注释对齐）：
//   - 返回切片首元素 == tierPlan 在候选池内且被 prefs 命中的首个元素；
//     若 prefs 中无元素在池，则首元素 == tierPlan 自身首元素（保序）。
//   - tierPlan 为空时返回 nil（语义：没有可恢复链，强造只是污染）。
//   - 跨 prefs/tierPlan 去重；空字符串视为不存在（跳过）。

// withRoleFailoverHead 把 prefs 中仍在候选池者按 role 顺序打头，tierPlan
// 保序去重随后。成员资格以候选池为准——被 tier 滤除后靠豁免复活的偏好
// （不在 tierPlan 里）由此进入恢复链。
func withRoleFailoverHead(tierPlan, prefs []string, candidates []ScoredCandidate) []string {
	if len(tierPlan) == 0 {
		return nil
	}
	inPool := make(map[string]struct{}, len(candidates))
	for i := range candidates {
		inPool[candidates[i].Candidate.CanonicalName] = struct{}{}
	}
	out := make([]string, 0, len(tierPlan)+len(prefs))
	seen := make(map[string]struct{}, len(tierPlan)+len(prefs))
	add := func(name string) {
		if name == "" {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, want := range prefs {
		if _, ok := inPool[want]; ok {
			add(want)
		}
	}
	for _, name := range tierPlan {
		add(name)
	}
	return out
}
