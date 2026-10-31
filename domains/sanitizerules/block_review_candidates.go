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

// EgressRuleEventWindow 是**一条审核单**的候选查询窗口。
//
// ★ Index 是调用方给的序号（回填时靠它对上审核单；★ 不是数据库主键 ★）。
type EgressRuleEventWindow struct {
	Index int
	From  time.Time
	To    time.Time
}

// ListEgressRuleEventsForWindows **逐单**取候选规则（★ 2026-10-31 拦截归因轮 ★）。
//
// ★★ 为什么不能继续用「整批一个窗口 + 一个全局 LIMIT」★★：
//
//	★ 旧读面（ListEgressRuleEventsInWindow）把**整批审核单**的时间并成一个
//	窗口，再按 `ORDER BY last_seen DESC LIMIT 50` 取**最近**的 50 条 ⇒ ★★
//	批次一旦跨时段（★ 211 生产实测：★ 24 条待审单横跨 **40 小时** ★）⇒
//	★★ 50 条预算被**最新**的事件吃光 ⇒ ★★ 早的那些审核单**一条候选都没有**★★
//	⇒ ★★ 而且「撞到上限」被当成**整批**截断 ⇒ ★★ 归因档位一律降级成
//	「unknown（请人工判断）」★★ ⇒ ★★ 读时归因——★ 整个「让历史空白单可判读」
//	的机制——**在这批数据上等于没生效** ★★。
//
// ⇒ ★ 修法：**每个审核单一个窗口**，一次往返（★ LATERAL + unnest ★），
//	**逐单预算**（★ 撞到上限只影响那一条 ★）★。
//
// ★ 返回值按窗口 Index 归组；★ 一个窗口都没有候选时**不**出现在 map 里
// （★ 调用方据此区分「没查到」与「查了但空」——★ 后者由 truncated 表达 ★）。
func ListEgressRuleEventsForWindows(ctx context.Context, tenantIDs []string, windows []EgressRuleEventWindow, perWindowLimit int) (map[int][]EgressRuleEventInWindow, error) {
	out := map[int][]EgressRuleEventInWindow{}
	p := reviewPersistenceInstalled()
	if p == nil {
		return out, nil
	}
	if len(windows) == 0 || len(tenantIDs) == 0 {
		// ★ 租户门 fail-closed（★ 同 ListEgressRuleEventsInWindow ★）。
		return out, nil
	}
	if perWindowLimit <= 0 || perWindowLimit > 200 {
		perWindowLimit = 50
	}
	usable := make([]EgressRuleEventWindow, 0, len(windows))
	for _, w := range windows {
		if w.From.IsZero() || w.To.IsZero() || !w.To.After(w.From) {
			continue
		}
		usable = append(usable, w)
	}
	if len(usable) == 0 {
		return out, nil
	}
	return p.ListEgressRuleEventsForWindowsDB(ctx, tenantIDs, usable, perWindowLimit)
}
