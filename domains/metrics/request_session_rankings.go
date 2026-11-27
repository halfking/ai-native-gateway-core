package metrics

import (
	"context"
	"fmt"
	"time"
)

type SessionRequestRankingQuery struct {
	Start, End time.Time
	Tenant     string
	// Owner 非空时按 owner_user 收窄（2026-11-27 非管理员菜单重构轮：
	// user 档恒注入本人 username ⇒ 五面板只算自己的行）。
	Owner         string
	SortBy, Order string
	Dimension     string
}

// SessionRequestRankingObservableWhereForTest 暴露 observable CTE 的 WHERE
// 文本给 admin 包的收窄判据（TestSessionRankingScopeUserTier 变异守卫用）：
// owner 收窄行被删 ⇒ 判据红。生产查询路径不消费它。
const SessionRequestRankingObservableWhereForTest = `($4='' OR owner_user=$4)`

const sessionRequestRankingBase = `
WITH projection AS (
	SELECT backfill_complete, updated_at,
		(SELECT min(enqueued_at) FROM public.request_metrics_pending_events
		 WHERE occurred_at >= $1 AND occurred_at < $2 AND ($3='' OR tenant_id=$3)) AS pending_since
	FROM public.request_metrics_repair_state WHERE singleton
), observable AS (
	SELECT tenant_id, owner_user, occurred_at, status, total_tokens, cached_tokens, latency_ms, credits_charged,
		nullif(session_id,'') AS session_id, nullif(agent_name,'') AS agent_name,
		CASE WHEN task_id <> 'default' AND task_id NOT LIKE 'auto-title:%' THEN nullif(task_id,'') END AS task,
		CASE WHEN cost_currency='USD' THEN cost_amount END AS usd,
		coalesce(nullif(provider_code,''),provider_id::text,'') AS provider, ` + requestModelKey + ` AS model
	FROM public.request_metrics_projection
	WHERE occurred_at >= $1 AND occurred_at < $2 AND ($3='' OR tenant_id=$3)
	  AND ($4='' OR owner_user=$4)
	  AND traffic_class='business' AND source_fact_id IS NOT NULL AND (SELECT backfill_complete FROM projection)
)
`

const sessionProviderModelRankings = `,
groups AS (
	SELECT provider,%s,
		count(*) AS request_count, count(*) FILTER (WHERE status='success') AS success_count,
		100.0*count(*) FILTER (WHERE status='success')/count(*) AS success_rate,
		coalesce(sum(total_tokens),0) AS total_tokens,
		count(*) FILTER (WHERE cached_tokens>0) AS cache_hit_count,
		100.0*count(*) FILTER (WHERE cached_tokens>0)/count(*) AS cache_rate,
		avg(latency_ms) FILTER (WHERE latency_ms>0) AS avg_duration_ms,
		sum(usd) AS total_cost, coalesce(sum(credits_charged),0) AS credits_charged,
		count(*)-count(usd) AS usd_unmeasured_requests,
		count(DISTINCT (tenant_id,owner_user)) FILTER (WHERE nullif(owner_user,'') IS NOT NULL) AS user_count,
		max(occurred_at) AS last_used_at
	FROM observable GROUP BY %s
)
`

const sessionAgentRankings = `,
agent_sessions AS (
	SELECT DISTINCT agent_name,tenant_id,session_id FROM observable WHERE agent_name IS NOT NULL AND session_id IS NOT NULL
), durations AS (
	SELECT a.agent_name,avg(extract(epoch FROM (d.last_active_at-d.first_request_at))) AS avg_duration_seconds
	FROM agent_sessions a JOIN public.session_dim d ON d.tenant_id=a.tenant_id AND d.gw_session_id=a.session_id
	WHERE d.last_active_at >= d.first_request_at GROUP BY a.agent_name
), activity AS (
	SELECT agent_name,count(DISTINCT (tenant_id,session_id)) FILTER (WHERE session_id IS NOT NULL) AS session_count,
		count(DISTINCT (tenant_id,owner_user)) FILTER (WHERE nullif(owner_user,'') IS NOT NULL) AS user_count,
		count(DISTINCT (tenant_id,task)) FILTER (WHERE task IS NOT NULL) AS task_count,
		count(*) AS request_count,coalesce(sum(total_tokens),0) AS total_tokens,
		sum(usd) AS total_cost,coalesce(sum(credits_charged),0) AS credits_charged,
		count(*)-count(usd) AS usd_unmeasured_requests,max(occurred_at) AS last_used_at
	FROM observable WHERE agent_name IS NOT NULL GROUP BY agent_name
), groups AS (
	SELECT a.*,d.avg_duration_seconds FROM activity a LEFT JOIN durations d USING (agent_name)
)
`

const sessionTaskRankings = `,
groups AS (
	SELECT task,count(DISTINCT (tenant_id,session_id)) FILTER (WHERE session_id IS NOT NULL) AS session_count,
		count(DISTINCT (tenant_id,owner_user)) FILTER (WHERE nullif(owner_user,'') IS NOT NULL) AS owner_count,
		count(*) AS turn_count,coalesce(sum(total_tokens),0) AS total_tokens,
		sum(usd) AS total_cost,coalesce(sum(credits_charged),0) AS credits_charged,
		count(*)-count(usd) AS usd_unmeasured_requests,max(occurred_at) AS last_active_at
	FROM observable WHERE task IS NOT NULL GROUP BY task
)
`

const sessionRequestRankingResult = `,
ranked AS (
	SELECT *,row_number() OVER (ORDER BY %s %s NULLS LAST,%s) AS rank FROM groups
)
SELECT backfill_complete,updated_at,pending_since,
	coalesce((SELECT jsonb_agg(to_jsonb(ranked) ORDER BY rank) FROM ranked),'[]')
FROM projection
`

var sessionRankingOrders = map[string]map[string]string{
	"models":    {"": "request_count", "requests": "request_count", "tokens": "total_tokens", "cost": "total_cost", "last_used": "last_used_at", "success_rate": "success_rate", "cache_rate": "cache_rate"},
	"providers": {"": "request_count", "requests": "request_count", "tokens": "total_tokens", "cost": "total_cost", "last_used": "last_used_at", "models": "model_count", "users": "user_count"},
	"agents":    {"": "session_count", "sessions": "session_count", "users": "user_count", "cost": "total_cost", "last_used": "last_used_at"},
	"tasks":     {"": "session_count", "sessions": "session_count", "turns": "turn_count", "tokens": "total_tokens", "cost": "total_cost", "users": "owner_count", "last_active": "last_active_at"},
}

func ValidSessionRankingSort(dimension, sortBy string) bool {
	return sessionRankingOrders[dimension][sortBy] != ""
}

func ReadSessionRequestRanking(ctx context.Context, db requestStatsDB, query SessionRequestRankingQuery) (ProjectedAccountActivity, error) {
	var result ProjectedAccountActivity
	orderColumn := sessionRankingOrders[query.Dimension][query.SortBy]
	if orderColumn == "" || !query.End.After(query.Start) {
		return result, fmt.Errorf("invalid session request ranking sort or window")
	}
	orderDirection := "DESC"
	if query.Order == "asc" {
		orderDirection = "ASC"
	}
	var group, ties string
	switch query.Dimension {
	case "models":
		ties = "provider,model"
		group = fmt.Sprintf(sessionProviderModelRankings, "model", ties)
	case "providers":
		ties = "provider"
		group = fmt.Sprintf(sessionProviderModelRankings, "count(DISTINCT nullif(model,'__unknown__')) AS model_count", ties)
	case "agents":
		group, ties = sessionAgentRankings, "agent_name"
	case "tasks":
		group, ties = sessionTaskRankings, "task"
	}
	statement := sessionRequestRankingBase + group + fmt.Sprintf(sessionRequestRankingResult, orderColumn, orderDirection, ties)
	err := db.QueryRow(ctx, statement, query.Start, query.End, query.Tenant, query.Owner).
		Scan(&result.Ready, &result.UpdatedAt, &result.PendingSince, &result.Payload)
	return result, err
}
