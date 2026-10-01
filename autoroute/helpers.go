package autoroute

// helpers.go — R50 F14（2026-09-21）从 role_llm_router.go 抽出的、与
// RoleLLMRouter 状态无关的 tier-failover 拼接小函数。搬迁理由：
//   - 函数本身只依赖 ScoredCandidate + []string，无 router/snapshot 状态，
//     长期挂在 role_llm_router.go 会误导后续维护者（误以为是 role 路由内部
//     实现而错改 R50 F14 契约）。
//   - 独立的 helpers.go 便于两个调用站点（V2 role promote decision_v2.go、
//     V2 pin promote decision_v2.go）共享文档与单测；V1 decision.go 走
//     promoteFirstPresent，待 P1-P5 plan 的 P3 对齐后再接入复用。
//
// R52（2026-10-01）新增 withMainstreamFallback（同属"状态无关的偏好拼接"
// 族，与数据表 builtinMainstreamFallback 分居两处：表在 role_llm_router.go
// 与 builtinRoleLLMPreference 镜像，函数在此）。
//
// 行为契约（务必与 R50 F14 注释对齐）：
//   - 返回切片首元素 == prefs 中在候选池内的首个元素（该元素可能在也可能
//     不在 tierPlan 内——被 tier 滤除后靠豁免复活的偏好同样打头）；
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

// withMainstreamFallback（R52, 2026-10-01）把主流兜底层追加到 kind 偏好
// 尾部，形成"整层不可用才进下一层"的逐层顺序：
//
//	[轻量层..., 主流层...]  →  promoteFirstPresent 顺序扫描
//
// 关键不变量：
//   - **只追加、不插队**：主流层排在全部 kind 偏好之后。轻量层有任一
//     模型在池内，就仍然先选低价模型——追加层不会抢占首选。
//   - **跨层去重**：重量层 kind（analysis/planning/solution）的偏好本身
//     就在主流池内，重复项只保留首次出现处，避免同一模型在 tier 豁免
//     名单里出现两次。
//   - **不修改入参**：SelectLLM 已返回副本，这里再拷一次，切断"兜底层
//     拼接会不会污染 DB 快照切片"的疑问。
//   - 关闭兜底层时调用方不调用本函数（开关见
//     FeatureFlags.AutoRoleMainstreamFallback）。
func withMainstreamFallback(prefs []string) []string {
	if len(prefs) == 0 {
		return nil
	}
	out := make([]string, 0, len(prefs)+len(builtinMainstreamFallback))
	seen := make(map[string]struct{}, len(prefs)+len(builtinMainstreamFallback))
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
		add(want)
	}
	for _, name := range builtinMainstreamFallback {
		add(name)
	}
	return out
}
