package autoroute

import (
	"context"
	"strings"
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

// goalShadowActorPrefix prefixes every goal follow-up actor
// (response_interceptor_helpers.followUpSourceActor).
const goalShadowActorPrefix = "goal-"

// internalLoopbackActors are the X-Gw-Is-Auto loopback emitters
// (telemetry.IsInternalAutoEntry's actor set).
var internalLoopbackActors = map[string]struct{}{
	"auto-title-generator":   {},
	"auto-summary-generator": {},
	"session-summary":        {},
}

// IsSyntheticActor reports whether origin_actor denotes a gateway-synthetic
// round. Empty actor (ordinary user traffic) is never synthetic.
func IsSyntheticActor(originActor string) bool {
	a := strings.TrimSpace(originActor)
	if a == "" {
		return false
	}
	if _, ok := internalLoopbackActors[a]; ok {
		return true
	}
	return strings.HasPrefix(a, goalShadowActorPrefix)
}

// SQLExcludeSyntheticActors returns the SQL predicate (safe to append inside
// a WHERE clause — each condition starts with AND) excluding the synthetic
// rounds above. alias may be "" for unaliased column references.
func SQLExcludeSyntheticActors(alias string) string {
	col := "origin_actor"
	if alias != "" {
		col = alias + ".origin_actor"
	}
	return " AND COALESCE(" + col + ", '') NOT LIKE 'goal-%'" +
		" AND COALESCE(" + col + ", '') NOT IN ('auto-title-generator','auto-summary-generator','session-summary')"
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
