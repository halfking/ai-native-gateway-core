package autoroute

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/internal/internaltraffic"
)

// R37 (2026-09-17) — R35-R2 灰度口径: gateway-synthetic rounds are excluded
// from every auto-route observation / training face (llmgw_autoroute_outcome_total,
// decision-time feedback stash, tuning signals, settle baselines & retry
// aggregates, work-type/top-model usage stats). They are real auto DECISIONS
// but not user-driven traffic: the goal shadow rounds (origin_actor
// goal-continue / goal-model-switch / goal-audit) are the gateway auditing
// its own conversations, and the internal loopbacks
// (auto-title-generator / auto-summary-generator / session-summary) are the
// title/summary self-calls. Both stay in request_logs / session_turns for
// auditability (doc 18: 打标不排除，过滤权在查询侧) — only the aggregate
// faces skip them.

// ⚠ 2026-10-03（审计 §9.73）：本文件原先**自己**硬编码了三个生成器 actor 名
// 与 `goal-` 前缀，而 `telemetry/internal_loopback.go` 与
// `db/request_logs_view_schema.go` 各有一份**一模一样**的拷贝，三者之间
// 没有任何依赖边 ⇒ 「改了一处、忘了另两处」在编译期完全等价。
// 现在三处都从 `internal/internaltraffic` 取，那里有唯一一份。
// 本文件保留导出 API（调用点遍布 bg / admin / autoroute 内部），只把**事实**委托出去。
// 谓词的**臂集不变**（actor ∪ goal-%，刻意不含 taskless 臂——理由见该包文档）。

// goalShadowActorPrefix 已移入 internal/internaltraffic.GoalShadowActorPrefix。
// 保留这个常量是为了让本文件里**仍然引用它的地方**编译通过；不要在此处再加新的
// 字面量——那正是本轮要消灭的东西。

// IsSyntheticActor reports whether origin_actor denotes a gateway-synthetic
// round. Empty actor (ordinary user traffic) is never synthetic.
func IsSyntheticActor(originActor string) bool {
	return internaltraffic.IsSyntheticActor(originActor)
}

// SQLExcludeSyntheticActors returns the SQL predicate (safe to append inside
// a WHERE clause — each condition starts with AND) excluding the synthetic
// rounds above. alias may be "" for unaliased column references.
func SQLExcludeSyntheticActors(alias string) string {
	col := "origin_actor"
	if alias != "" {
		col = alias + ".origin_actor"
	}
	// R39 note: the SQL predicates deliberately do NOT BTRIM. The Go side
	// (IsSyntheticActor) trims first, so a value with surrounding whitespace
	// would diverge between the two — this is safe only because BOTH write
	// entries TrimSpace before the value lands in request_logs_hot:
	// middleware/origin_mw.go (X-LLM-Origin-Actor, user traffic) and
	// domains/streaming/request_log_pipeline.go (X-Gw-Source-Actor,
	// loopback/goal chain, R42 correction — the R39 text claimed a sole
	// writer). Loopback actors are additionally code constants. If another
	// writer ever touches origin_actor, trim there too; do not "fix" just
	// one side of this pair.
	_ = col
	return internaltraffic.SQLExcludeSyntheticActors(alias)
}

// originActorContextKey carries the request's origin actor into the Decider
// so recordFeedbackAsync can skip synthetic rounds at decision time (the
// terminal side filters inside ReportRoutingOutcome).
type originActorContextKey struct{}

// WithOriginActor returns a context carrying the request's origin actor.
// Set by the streaming entry points next to WithRequestID.
func WithOriginActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, originActorContextKey{}, actor)
}

func originActorFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(originActorContextKey{}).(string)
	return v
}
