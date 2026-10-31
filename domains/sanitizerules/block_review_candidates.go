package sanitizerules

import (
	"context"
	"time"
)

// ★ 审核单的**候选规则**读面（2026-10-29 输出合规误报轮 · 「看不到原始上下文，
// 搞不明白为什么被拦」）。
//
// ★ 为什么需要它：★ 审核单的「命中规则」字段来自拦截点自报 ⇒ ★★ 历史记录全空
//   ⇒ ★★ 审核员无法知道这次拦截是不是内容规则命中 ⇒ ★★ 于是「守卫侧拦截」
//   （守卫装不下 ⇒ 被当成违规）与「内容规则命中」长得一模一样 ★★
//   ⇒判定用两个**事实**：★ ① 请求日志的失败口径（出站侧拦截）；★ ② **邻近
//   时段有没有内容规则命中** —— ★★ 没有 ⇒ 守卫侧；★ 有 ⇒ 只给**候选** ★★。
//
// ★ 口径说明：★ `sanitize_rule_events` 是**按指纹聚合**的（★ 同一规则反复命中
//   聚成一行，first_seen/last_seen 覆盖一段时间）⇒ ★★ 窗口重叠是**近似**
//   （★ 不是 per-request 归因）⇒ ★★ 因此这里返回的一切都只能叫「候选」★★。

// EgressRuleEventInWindow 是一条落在时间窗内的出站规则命中事件。
type EgressRuleEventInWindow struct {
	RuleID    string    `json:"rule_id"`
	Action    string    `json:"action,omitempty"`
	Status    string    `json:"status,omitempty"`
	Count     int64     `json:"count,omitempty"`
	FirstSeen time.Time `json:"first_seen,omitempty"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
}

// ListEgressRuleEventsInWindow 取与 [from, to] 有重叠的**出站侧**规则命中事件。
//
// ★ 只取 phase='egress'（★ 出站侧 = 拦截发生的那一侧）★★
// ★ limit 有界（★ 默认 50 ⇒候选不会刷屏）★★
//
// ★★ tenantIDs（2026-10-30 12h 审计轮）★★：★★ 租户是**必填**门 ★★
// ⇒ ★★ 原先这个读面**没有任何租户条件** ⇒ ★★ 候选来自**全租户** ⇒ ★★
// 租户 A 的审核员看到 B 的规则 ID / action / count ⇒ ★★ 且 A 的归因结论
// 被 B 的命中记录左右 ⇒ ★★ ⇒ ★★ 这里必须 fail-closed：★★
// tenantIDs 为空 ⇒ ★★ 直接返回空（★ 不允许「空 = 不限」★★）。
func ListEgressRuleEventsInWindow(ctx context.Context, tenantIDs []string, from, to time.Time, limit int) ([]EgressRuleEventInWindow, error) {
	p := reviewPersistenceInstalled()
	if p == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return nil, nil
	}
	if len(tenantIDs) == 0 {
		return nil, nil
	}
	return p.ListEgressRuleEventsInWindowDB(ctx, tenantIDs, from, to, limit)
}
