package metrics

import (
	"context"
	"fmt"
	"time"
)

type SessionAnalyticsOverviewQuery struct {
	Start, End, PreviousStart time.Time
	Tenant, Timezone          string
}

const sessionAnalyticsOverviewSQL = `
WITH projection AS (
	SELECT backfill_complete,updated_at,
		(SELECT min(enqueued_at) FROM public.request_metrics_pending_events
		 WHERE occurred_at >= $3 AND occurred_at < $2 AND ($4='' OR tenant_id=$4)) AS pending_since
	FROM public.request_metrics_repair_state WHERE singleton
), periods AS (
	SELECT true AS current,$1::timestamptz AS lo,$2::timestamptz AS hi
	UNION ALL SELECT false,$3::timestamptz,$1::timestamptz
), observed AS MATERIALIZED (
	SELECT m.tenant_id,nullif(m.session_id,'') AS session_id,nullif(m.owner_user,'') AS owner,
		m.occurred_at >= $1 AS current,(m.occurred_at AT TIME ZONE $5)::date AS day,
		public.gw_resolve_project_ref(d.project_id,coalesce(nullif(d.application_code,''),m.application_code)) AS project,
		CASE WHEN m.task_id <> 'default' AND m.task_id NOT LIKE 'auto-title:%' THEN nullif(m.task_id,'') END AS task,
		` + requestModelKey + ` AS model,m.total_tokens,m.cached_tokens,m.status,m.credits_charged,
		CASE WHEN m.cost_currency='USD' THEN m.cost_amount END AS usd
	FROM public.request_metrics_projection m
	LEFT JOIN public.session_dim d ON d.tenant_id=m.tenant_id AND d.gw_session_id=m.session_id
	WHERE m.occurred_at >= $3 AND m.occurred_at < $2 AND ($4='' OR m.tenant_id=$4)
		AND m.traffic_class='business' AND m.source_fact_id IS NOT NULL AND (SELECT backfill_complete FROM projection)
), touched AS (
	SELECT current,tenant_id,session_id FROM observed WHERE session_id IS NOT NULL
	UNION
	SELECT p.current,d.tenant_id,d.gw_session_id FROM periods p JOIN public.session_dim d
	  ON (d.first_request_at >= p.lo AND d.first_request_at < p.hi) OR (d.closed_at >= p.lo AND d.closed_at < p.hi)
	WHERE ($4='' OR d.tenant_id=$4) AND (SELECT backfill_complete FROM projection)
	  AND EXISTS (SELECT 1 FROM public.request_metrics_projection r WHERE r.tenant_id=d.tenant_id AND r.session_id=d.gw_session_id
		AND r.occurred_at<p.hi AND r.traffic_class='business' AND r.source_fact_id IS NOT NULL)
), summary_sessions AS (
	SELECT t.current,count(*) AS sessions,
		avg(extract(epoch FROM d.last_active_at-d.first_request_at))
			FILTER (WHERE d.last_active_at>=d.first_request_at) AS duration
	FROM touched t LEFT JOIN public.session_dim d ON d.tenant_id=t.tenant_id AND d.gw_session_id=t.session_id
	GROUP BY t.current
), request_measures AS (
	SELECT current,count(*) AS requests,count(usd) AS measured_usd,sum(usd) AS usd,
		coalesce(sum(credits_charged),0) AS credits,
		count(DISTINCT (tenant_id,owner)) FILTER (WHERE owner IS NOT NULL) AS users,
		count(DISTINCT (tenant_id,project)) FILTER (WHERE project IS NOT NULL) AS projects,
		count(DISTINCT (tenant_id,task)) FILTER (WHERE task IS NOT NULL) AS tasks,
		sum(total_tokens) FILTER (WHERE session_id IS NOT NULL) AS session_tokens,
		count(*) FILTER (WHERE session_id IS NOT NULL) AS session_requests,
		count(DISTINCT (tenant_id,session_id)) FILTER (WHERE session_id IS NOT NULL) AS active_sessions,
		100.0*count(*) FILTER (WHERE status='success')/count(*) AS success_rate,
		100.0*count(*) FILTER (WHERE cached_tokens>0)/count(*) AS cache_rate
	FROM observed GROUP BY current
), totals AS (
	SELECT p.current,coalesce(s.sessions,0) AS sessions,s.duration,coalesce(r.users,0) AS users,
		coalesce(r.projects,0) AS projects,coalesce(r.tasks,0) AS tasks,
		coalesce(r.requests,0) AS requests,coalesce(r.measured_usd,0) AS measured_usd,
		CASE WHEN r.requests IS NULL THEN 0 ELSE r.usd END AS usd,coalesce(r.credits,0) AS credits,
		r.session_tokens::numeric/nullif(r.active_sessions,0) AS avg_tokens,
		r.session_requests::numeric/nullif(r.active_sessions,0) AS avg_turns,r.success_rate,r.cache_rate
	FROM periods p LEFT JOIN summary_sessions s USING (current) LEFT JOIN request_measures r USING (current)
), daily AS (
	SELECT day,count(DISTINCT (tenant_id,session_id)) FILTER (WHERE session_id IS NOT NULL) AS sessions,
		sum(usd) AS usd,coalesce(sum(credits_charged),0) AS credits,coalesce(sum(total_tokens),0) AS tokens
	FROM observed WHERE current GROUP BY day
), top_models AS (
	SELECT model,count(*) AS count FROM observed WHERE current AND model<>'__unknown__'
	GROUP BY model ORDER BY count DESC,model LIMIT 3
), owner_totals AS (
	SELECT tenant_id,owner,count(DISTINCT session_id) AS session_count,coalesce(sum(total_tokens),0) AS total_tokens,
		sum(usd) AS total_cost,coalesce(sum(credits_charged),0) AS credits_charged
	FROM observed WHERE current AND owner IS NOT NULL GROUP BY tenant_id,owner
	ORDER BY total_tokens DESC,tenant_id,owner LIMIT 10
), top_owners AS (
	SELECT o.*,coalesce(nullif(u.display_name,''),o.owner) AS display_name,coalesce(u.email,'') AS email
	FROM owner_totals o LEFT JOIN public.users u ON u.tenant_id=o.tenant_id AND u.username=o.owner
), top_projects AS (
	SELECT project,count(DISTINCT (tenant_id,session_id)) FILTER (WHERE session_id IS NOT NULL) AS session_count,
		count(DISTINCT (tenant_id,owner)) FILTER (WHERE owner IS NOT NULL) AS user_count,
		sum(usd) AS total_cost,coalesce(sum(credits_charged),0) AS credits_charged
	FROM observed WHERE current AND project IS NOT NULL GROUP BY project
	ORDER BY session_count DESC,project LIMIT 10
)
SELECT backfill_complete,updated_at,pending_since,
	jsonb_build_object('summary',jsonb_build_object(
		'total_sessions',c.sessions,'total_sessions_growth',100.0*(c.sessions-p.sessions)/nullif(p.sessions,0),
		'avg_tokens',c.avg_tokens,'avg_context_size',NULL::numeric,'avg_duration_seconds',c.duration,'avg_turns',c.avg_turns,
		'request_count',c.requests,'total_cost_usd',c.usd,'credits_charged',c.credits,
		'total_cost_usd_growth',CASE WHEN c.requests=c.measured_usd AND p.requests=p.measured_usd
			THEN 100.0*(c.usd-p.usd)/nullif(p.usd,0) END,
		'usd_unmeasured_requests',c.requests-c.measured_usd,'active_users',c.users,
		'active_users_growth',100.0*(c.users-p.users)/nullif(p.users,0),
		'active_projects',c.projects,'active_tasks',c.tasks,'overall_success_rate',c.success_rate,'overall_cache_rate',c.cache_rate),
		'trends',jsonb_build_object(
			'sessions',coalesce((SELECT jsonb_agg(jsonb_build_object('date',day,'count',sessions) ORDER BY day) FROM daily),'[]'),
			'tokens',coalesce((SELECT jsonb_agg(jsonb_build_object('date',day,'total',tokens) ORDER BY day) FROM daily),'[]'),
			'cost',coalesce((SELECT jsonb_agg(jsonb_build_object('date',day,'total_cost_usd',usd,'credits_charged',credits) ORDER BY day) FROM daily),'[]')),
		'top_models',coalesce((SELECT jsonb_agg(to_jsonb(top_models) ORDER BY count DESC,model) FROM top_models),'[]'),
		'top_users',coalesce((SELECT jsonb_agg(to_jsonb(top_owners) ORDER BY total_tokens DESC,tenant_id,owner) FROM top_owners),'[]'),
		'top_projects',coalesce((SELECT jsonb_agg(to_jsonb(top_projects) ORDER BY session_count DESC,project) FROM top_projects),'[]'))
FROM projection CROSS JOIN totals c CROSS JOIN totals p WHERE c.current AND NOT p.current`

func ReadSessionAnalyticsOverview(ctx context.Context, db requestStatsDB, query SessionAnalyticsOverviewQuery) (ProjectedAccountActivity, error) {
	var result ProjectedAccountActivity
	if !query.End.After(query.Start) || !query.Start.After(query.PreviousStart) {
		return result, fmt.Errorf("invalid session analytics comparison window")
	}
	err := db.QueryRow(ctx, sessionAnalyticsOverviewSQL, query.Start, query.End, query.PreviousStart, query.Tenant, query.Timezone).
		Scan(&result.Ready, &result.UpdatedAt, &result.PendingSince, &result.Payload)
	return result, err
}
