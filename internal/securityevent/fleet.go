package securityevent

// 跨实例威胁模式提示（M2-3）的序列化。
//
// 提示本身是**观测数据**：它让一条事件在共享域里自带"别的实例也见过这个
// 模式"的视角。它不参与任何拦截判定（见 Event.FleetHint 的说明）。

import "time"

// FleetHintMap 把提示摊成可写入 metadata 的 map。
//
// nil ⇒ 返回 nil（写面据此**不写这个键**，而不是写一个空的提示 ——
// 空提示会被读成"全网只有我见过"，那是错的）。
func FleetHintMap(hint *FleetHint) map[string]any {
	if hint == nil {
		return nil
	}
	instances := hint.Instances
	if instances == nil {
		instances = []string{}
	}
	return map[string]any{
		"signature":      hint.Signature,
		"observations":   hint.Observations,
		"instances":      instances,
		"instance_count": hint.InstanceCount,
		"fetched_at":     hint.FetchedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// FleetHintTags 返回提示对应的标签。
//
// 标签是 Qdrant 的数组精确匹配（buildQdrantFilter 把每个 tag 变成一条
// must 条件）⇒ 消费侧可以用 filters.tags 精确拉到"带全网视角的事件"。
//
// 只加一个固定标签 fleet:known —— 模式签名里带 "|" 与 "."，放进标签会
// 让匹配规则难写；真正的模式区分用 metadata 里的 signature。
func FleetHintTags(hint *FleetHint) []string {
	if hint == nil {
		return nil
	}
	return []string{"fleet:known"}
}

// FleetHintFetchedAt 供消费侧判断快照新鲜度。
//
// 零值表示"没有快照" —— 零值不得被当成"刚刚取回"。
func FleetHintFetchedAt(hint *FleetHint) time.Time {
	if hint == nil {
		return time.Time{}
	}
	return hint.FetchedAt
}
