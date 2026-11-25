package metrics

import (
	"context"
	"fmt"
	"time"
)

type SessionDashboardQuery struct {
	Start, End, PrevStart time.Time
	RecentStart, AsOf     time.Time
	Timezone              string
	Tenant, Owner         string
	IncludeProbes         bool
	Page, Size            int
}

type SessionDashboardSection string

const (
	SessionDashboardOverview SessionDashboardSection = "overview"
	SessionDashboardTrend    SessionDashboardSection = "trend"
	SessionDashboardActive   SessionDashboardSection = "active"
	SessionDashboardHealth   SessionDashboardSection = "health"
)

// Session counts use (tenant, session). Request metrics use occurred_at;
// creation and closure come from the native session clock.
const sessionDashboardBase = `
WITH projection AS (
	SELECT backfill_complete, updated_at,
		CASE WHEN backfill_complete THEN
			(SELECT min(enqueued_at) FROM public.request_metrics_pending_events
			 WHERE occurred_at >= $1 AND occurred_at < $2 AND ($4='' OR tenant_id=$4))
		END AS pending_since
	FROM public.request_metrics_repair_state WHERE singleton
), selected AS MATERIALIZED (
	SELECT m.tenant_id, nullif(m.session_id,'') AS session_id, m.occurred_at,
		` + requestModelKey + ` AS model,
		nullif(m.client_id,'') AS client_id,
		CASE WHEN m.task_id <> 'default' AND m.task_id NOT LIKE 'auto-title:%' THEN nullif(m.task_id,'') END AS task_id,
		m.status, m.total_tokens, m.credits_charged, m.latency_ms,
		CASE WHEN m.cost_currency='USD' THEN m.cost_amount END AS usd
	FROM public.request_metrics_projection m
	WHERE m.occurred_at >= $1 AND m.occurred_at < $2 AND m.source_fact_id IS NOT NULL
	  AND ($4='' OR m.tenant_id=$4) AND ($6::boolean OR m.traffic_class='business')
	  AND ($5='' OR EXISTS (SELECT 1 FROM public.session_dim own WHERE own.tenant_id=m.tenant_id
		AND own.gw_session_id=m.session_id AND own.owner_user=$5))
	  AND (SELECT backfill_complete FROM projection)
), session_requests AS MATERIALIZED (
	SELECT * FROM selected WHERE session_id IS NOT NULL
), active AS (
	SELECT tenant_id, session_id, sum(usd) AS usd FROM session_requests GROUP BY tenant_id, session_id
), lifecycle AS MATERIALIZED (
	SELECT d.tenant_id, d.gw_session_id AS session_id, d.first_request_at, d.closed_at
	FROM public.session_dim d
	WHERE ($4='' OR d.tenant_id=$4) AND ($5='' OR d.owner_user=$5)
	  AND ((d.first_request_at >= $3 AND d.first_request_at < $2) OR (d.closed_at >= $1 AND d.closed_at < $2))
	  AND (SELECT backfill_complete FROM projection)
	  AND EXISTS (SELECT 1 FROM public.request_metrics_projection e WHERE e.tenant_id=d.tenant_id
		AND e.session_id=d.gw_session_id AND e.source_fact_id IS NOT NULL AND ($6 OR e.traffic_class='business')
		AND e.occurred_at < $2)
), daily_sessions AS (
	SELECT (occurred_at AT TIME ZONE $7)::date AS day, count(DISTINCT (tenant_id,session_id)) AS count,
		sum(usd) AS usd, count(*) AS requests, count(usd) AS usd_requests
	FROM session_requests GROUP BY 1
), daily_new AS (
	SELECT (first_request_at AT TIME ZONE $7)::date AS day, count(*) AS count
	FROM lifecycle WHERE first_request_at >= $1 AND first_request_at < $2 GROUP BY 1
), daily_closed AS (
	SELECT (closed_at AT TIME ZONE $7)::date AS day, count(*) AS count
	FROM lifecycle WHERE closed_at >= $1 AND closed_at < $2 GROUP BY 1
), days AS (
	SELECT date::date AS day FROM generate_series(($1::timestamptz AT TIME ZONE $7)::date,
		(($2::timestamptz - interval '1 microsecond') AT TIME ZONE $7)::date, '1 day'::interval) day_list(date)
), daily AS (
	SELECT to_char(days.day, 'YYYY-MM-DD') AS date, coalesce(n.count,0) AS new_sessions,
		coalesce(a.count,0) AS active_count, coalesce(c.count,0) AS closed_count,
		CASE WHEN a.requests IS NULL THEN 0 ELSE a.usd END AS total_cost,
		coalesce(a.requests,0) AS total_requests, coalesce(a.usd_requests,0) AS usd_requests
	FROM days LEFT JOIN daily_sessions a USING (day) LEFT JOIN daily_new n USING (day) LEFT JOIN daily_closed c USING (day)
), lifecycle_summary AS (
	SELECT count(*) FILTER (WHERE first_request_at >= $1 AND first_request_at < $2) AS new_count,
		count(*) FILTER (WHERE closed_at >= $1 AND closed_at < $2) AS closed_count,
		count(*) FILTER (WHERE first_request_at >= $8 AND first_request_at < $9) AS recent_new,
		count(*) FILTER (WHERE closed_at >= $8 AND closed_at < $9) AS recent_closed,
		count(*) FILTER (WHERE first_request_at >= $3 AND first_request_at < $1) AS previous_new
	FROM lifecycle
), trend_data AS (
	SELECT jsonb_build_object(
		'trend', coalesce((SELECT jsonb_agg(to_jsonb(daily) ORDER BY date) FROM daily),'[]'),
		'summary', jsonb_build_object('total_new', new_count, 'total_closed', closed_count,
			'total_active', (SELECT count(*) FROM active),
			'avg_daily_new', new_count::float8/nullif((SELECT count(*) FROM daily),0),
			'growth_rate_pct', CASE WHEN previous_new>0 THEN 100.0*(new_count-previous_new)/previous_new END,
			'growth_rate_known', previous_new>0)) AS value
	FROM lifecycle_summary
)
`

const sessionDashboardTrend = sessionDashboardBase + `
SELECT backfill_complete, updated_at, pending_since, value FROM projection CROSS JOIN trend_data`

const sessionDashboardCohort = sessionDashboardBase + `,
cohort AS (
	SELECT tenant_id, session_id FROM active
	UNION SELECT tenant_id, session_id FROM lifecycle
	WHERE (first_request_at >= $1 AND first_request_at < $2) OR (closed_at >= $1 AND closed_at < $2)
), session_labels AS MATERIALIZED (
	SELECT c.tenant_id, c.session_id, s.health_grade, s.health_score, s.compliance_status,
		s.prompt_injection_detected, s.pii_detected, s.toxic_output_detected, s.last_health_at
	FROM cohort c LEFT JOIN public.session_summaries s ON s.tenant_id=c.tenant_id AND s.session_key=c.session_id
), quality AS (
	SELECT count(*) AS sessions,
		count(*) FILTER (WHERE health_grade IN ('A','B','C','D','F')) AS graded,
		count(*) FILTER (WHERE health_grade='A') AS a, count(*) FILTER (WHERE health_grade='B') AS b,
		count(*) FILTER (WHERE health_grade='C') AS c, count(*) FILTER (WHERE health_grade='D') AS d,
		count(*) FILTER (WHERE health_grade='F') AS f, avg(health_score) AS avg_health,
		count(*) FILTER (WHERE compliance_status='warning') AS warning,
		count(*) FILTER (WHERE compliance_status='violation') AS violation,
		count(*) FILTER (WHERE prompt_injection_detected) AS prompt_injection,
		count(*) FILTER (WHERE pii_detected) AS pii,
		count(*) FILTER (WHERE toxic_output_detected) AS toxic
	FROM session_labels
), health_distribution AS (
	SELECT jsonb_build_object('total',graded,'not_assessed',sessions-graded,
		'a',a,'b',b,'c',c,'d',d,'f',f,'avg_score',avg_health,
		'a_percent',100.0*a/nullif(graded,0),'b_percent',100.0*b/nullif(graded,0),'c_percent',100.0*c/nullif(graded,0),
		'd_percent',100.0*d/nullif(graded,0),'f_percent',100.0*f/nullif(graded,0)) AS value
	FROM quality
)
`

const sessionDashboardOverview = sessionDashboardCohort + `,
attributed AS (
	SELECT (r.occurred_at AT TIME ZONE $7)::date AS day, r.total_tokens, r.credits_charged, r.usd,
		CASE WHEN r.session_id IS NULL THEN 'no_session'
		     WHEN d.gw_session_id IS NULL THEN 'missing_session_record'
		     WHEN nullif(btrim(d.owner_user),'') IS NULL THEN 'missing_session_owner'
		     WHEN d.first_request_at IS NULL THEN 'unknown_session_start'
		     WHEN d.first_request_at < $1 THEN 'opened_before_window'
		     ELSE 'other_session_requests' END AS reason
	FROM selected r LEFT JOIN public.session_dim d ON d.tenant_id=r.tenant_id AND d.gw_session_id=r.session_id
), attribution_groups AS (
	SELECT day,reason,count(*) AS requests,coalesce(sum(total_tokens),0) AS tokens,
		coalesce(sum(credits_charged),0) AS credits,sum(usd) AS cost_usd,count(usd) AS usd_requests
	FROM attributed GROUP BY GROUPING SETS ((day,reason),(reason))
), attribution AS (
	SELECT jsonb_build_object(
		'totals',coalesce(jsonb_agg(to_jsonb(a)-'day' ORDER BY reason) FILTER (WHERE day IS NULL),'[]'),
		'daily',coalesce(jsonb_agg(to_jsonb(a) ORDER BY day,reason) FILTER (WHERE day IS NOT NULL),'[]')) AS value
	FROM attribution_groups a
),
measures AS (
	SELECT count(*) AS requests, count(usd) AS usd_requests, sum(usd) AS usd,
		avg(usd) AS avg_request_cost, avg(latency_ms) FILTER (WHERE latency_ms>0) AS avg_latency,
		coalesce(sum(total_tokens),0) AS tokens, coalesce(sum(credits_charged),0) AS credits
	FROM session_requests
), previous_cost AS (
	SELECT sum(m.cost_amount) FILTER (WHERE m.cost_currency='USD') AS usd,
		count(*) FILTER (WHERE m.cost_currency IS DISTINCT FROM 'USD' OR m.cost_amount IS NULL) AS unmeasured
	FROM public.request_metrics_projection m
	WHERE m.occurred_at >= $3 AND m.occurred_at < $1 AND m.source_fact_id IS NOT NULL AND nullif(m.session_id,'') IS NOT NULL
	  AND ($4='' OR m.tenant_id=$4) AND ($6 OR m.traffic_class='business') AND (SELECT backfill_complete FROM projection)
	  AND ($5='' OR EXISTS (SELECT 1 FROM public.session_dim own WHERE own.tenant_id=m.tenant_id
		AND own.gw_session_id=m.session_id AND own.owner_user=$5))
), models AS (
	SELECT model, count(DISTINCT (tenant_id,session_id)) AS session_count, count(*) AS request_count,
		sum(usd) AS total_cost, count(usd) AS usd_requests, avg(latency_ms) FILTER (WHERE latency_ms>0) AS avg_latency_ms,
		count(*) FILTER (WHERE status='success')::float8/count(*) AS success_rate
	FROM session_requests GROUP BY model ORDER BY request_count DESC, model LIMIT 15
), dimension_sessions AS (
	SELECT dim, key, r.tenant_id, r.session_id, sum(r.usd) AS usd, max(r.occurred_at) AS last_activity
	FROM selected r CROSS JOIN LATERAL (VALUES ('client',r.client_id),('task',r.task_id)) d(dim,key)
	WHERE key IS NOT NULL GROUP BY dim,key,r.tenant_id,r.session_id
), rankings AS (
	SELECT dim, key, count(d.session_id) AS session_count, sum(d.usd) AS total_cost, round(avg(s.health_score))::integer AS avg_health,
		max(last_activity) AS last_activity,
		row_number() OVER (PARTITION BY dim ORDER BY sum(d.usd) DESC NULLS LAST, count(d.session_id) DESC, key) AS rank
	FROM dimension_sessions d LEFT JOIN session_labels s USING (tenant_id,session_id) GROUP BY dim,key
)
SELECT backfill_complete, updated_at, pending_since,
	jsonb_build_object('total_sessions', quality.sessions, 'active_sessions', (SELECT count(*) FROM active),
		'new_sessions', new_count, 'closed_sessions', closed_count, 'new_sessions_24h', recent_new, 'closed_sessions_24h', recent_closed,
		'requests_without_session', (SELECT count(*) FROM selected WHERE session_id IS NULL),
		'session_attribution', attribution.value,
		'health_distribution', health_distribution.value,
		-- The native compliant value is a schema default and has no assessment marker.
		'compliance_stats', jsonb_build_object('total',warning+violation,'not_assessed',quality.sessions-warning-violation,
			'compliant',NULL::integer,'warning',warning,'violation',violation,'prompt_injection_detected',prompt_injection,
			'pii_detected',pii,'toxic_output_detected',toxic,'compliance_rate',NULL::numeric),
		'cost_stats', jsonb_build_object('total_cost_usd',CASE WHEN measures.requests=0 THEN 0 ELSE measures.usd END,
			'avg_cost_per_session',(SELECT avg(usd) FROM active),'avg_cost_per_request',avg_request_cost,'max_cost_session',(SELECT max(usd) FROM active),
			'input_cost_usd',NULL::numeric,'output_cost_usd',NULL::numeric,
			'cost_growth_pct',CASE WHEN previous_cost.usd>0 AND previous_cost.unmeasured=0 AND measures.requests=measures.usd_requests
				THEN 100.0*(coalesce(measures.usd,0)-previous_cost.usd)/previous_cost.usd END,
			'total_requests',measures.requests,'total_tokens',tokens,'total_credits',credits,'usd_requests',usd_requests,'avg_latency_ms',avg_latency),
		'model_usage',coalesce((SELECT jsonb_agg(to_jsonb(models) ORDER BY request_count DESC,model) FROM models),'[]'),
		'top_clients',coalesce((SELECT jsonb_agg(jsonb_build_object('client_id',key,'session_count',session_count,
			'total_cost',total_cost,'avg_health',avg_health,'last_activity',last_activity) ORDER BY rank) FROM rankings WHERE dim='client' AND rank<=5),'[]'),
		'top_tasks',coalesce((SELECT jsonb_agg(jsonb_build_object('task_id',key,'session_count',session_count,
			'total_cost',total_cost,'avg_health',avg_health,'last_activity',last_activity) ORDER BY rank) FROM rankings WHERE dim='task' AND rank<=5),'[]'),
		'cost_trend',coalesce((SELECT jsonb_agg(jsonb_build_object('date',date,'cost',total_cost,
			'sessions',active_count,'requests',total_requests) ORDER BY date) FROM daily),'[]'),
		'session_trend',trend_data.value->'trend','trend_summary',trend_data.value->'summary')
FROM projection CROSS JOIN quality CROSS JOIN health_distribution CROSS JOIN measures CROSS JOIN attribution
CROSS JOIN lifecycle_summary CROSS JOIN trend_data CROSS JOIN previous_cost`

const sessionDashboardActive = sessionDashboardBase + `,
activity AS (
	SELECT tenant_id, session_id, count(*) AS request_count, count(usd) AS usd_requests,
		sum(usd) AS total_cost, max(occurred_at) AS last_active_at,
		(array_agg(model ORDER BY occurred_at DESC,model))[1] AS model,
		(array_agg(client_id ORDER BY occurred_at DESC,client_id) FILTER (WHERE client_id IS NOT NULL))[1] AS client_id
	FROM session_requests GROUP BY tenant_id,session_id
), page_rows AS MATERIALIZED (
	SELECT * FROM activity ORDER BY last_active_at DESC,tenant_id,session_id
	LIMIT $11::integer OFFSET least(($10::bigint-1)::numeric*$11::integer,9223372036854775807)::bigint
), items AS (
	SELECT p.session_id,p.session_id AS session_key,p.tenant_id,p.request_count,p.usd_requests,p.total_cost,p.last_active_at,
		nullif(p.model,'__unknown__') AS model, nullif(p.model,'__unknown__') AS primary_model,
		coalesce(p.client_id,nullif(d.client_id,'')) AS client_id, d.first_request_at AS created_at,
		s.health_score,s.health_grade
	FROM page_rows p
	LEFT JOIN public.session_dim d ON d.tenant_id=p.tenant_id AND d.gw_session_id=p.session_id
	LEFT JOIN public.session_summaries s ON s.tenant_id=p.tenant_id AND s.session_key=p.session_id
)
SELECT backfill_complete,updated_at,pending_since,jsonb_build_object(
	'total_active',(SELECT count(*) FROM activity),'page',$10::bigint,'size',$11::integer,
	'sessions',coalesce((SELECT jsonb_agg(to_jsonb(items) ORDER BY last_active_at DESC,tenant_id,session_id) FROM items),'[]'))
FROM projection`

const sessionDashboardHealth = sessionDashboardCohort + `,
health_trend AS (
	SELECT to_char((last_health_at AT TIME ZONE current_setting('TimeZone') AT TIME ZONE $7)::date,'YYYY-MM-DD') AS date,
		avg(health_score) AS avg_score, count(*) FILTER (WHERE health_grade='A') AS grade_a,
		count(*) FILTER (WHERE health_grade='F') AS grade_f
	FROM session_labels
	WHERE health_score IS NOT NULL AND last_health_at >= ($1::timestamptz AT TIME ZONE current_setting('TimeZone'))
	  AND last_health_at < ($2::timestamptz AT TIME ZONE current_setting('TimeZone'))
	GROUP BY 1
), request_issues AS (
	SELECT tenant_id,session_id,count(*) FILTER (WHERE status='failed') AS failures,
		avg(latency_ms) FILTER (WHERE latency_ms>0) AS avg_latency
	FROM session_requests GROUP BY tenant_id,session_id
), health_issues AS (
	SELECT issue,count(*) AS count,'warning' AS severity
	FROM session_labels s LEFT JOIN request_issues r USING (tenant_id,session_id)
	CROSS JOIN LATERAL (VALUES ('prompt_injection',s.prompt_injection_detected),('pii_detected',s.pii_detected),
		('toxic_output',s.toxic_output_detected),('high_error_rate',r.failures>5),('high_latency',r.avg_latency>5000)) i(issue,observed)
	WHERE observed GROUP BY issue ORDER BY count DESC,issue LIMIT 5
)
SELECT backfill_complete,updated_at,pending_since,jsonb_build_object(
	'distribution',health_distribution.value,
	'trend',coalesce((SELECT jsonb_agg(to_jsonb(health_trend) ORDER BY date) FROM health_trend),'[]'),
	'top_issues',coalesce((SELECT jsonb_agg(to_jsonb(health_issues) ORDER BY count DESC,issue) FROM health_issues),'[]'))
FROM projection CROSS JOIN health_distribution`

func ReadSessionDashboard(ctx context.Context, db requestStatsDB, query SessionDashboardQuery, section SessionDashboardSection) (ProjectedAccountActivity, error) {
	var result ProjectedAccountActivity
	if (query.Owner != "" && query.Tenant == "") || !query.End.After(query.Start) || !query.Start.After(query.PrevStart) {
		return result, fmt.Errorf("invalid session dashboard scope or window")
	}
	args := []any{query.Start, query.End, query.PrevStart, query.Tenant, query.Owner,
		query.IncludeProbes, query.Timezone, query.RecentStart, query.AsOf}
	var statement string
	switch section {
	case SessionDashboardOverview:
		statement = sessionDashboardOverview
	case SessionDashboardTrend:
		statement = sessionDashboardTrend
	case SessionDashboardActive:
		if query.Page < 1 || query.Size < 1 || query.Size > 100 {
			return result, fmt.Errorf("invalid active sessions pagination")
		}
		statement = sessionDashboardActive
		args = append(args, query.Page, query.Size)
	case SessionDashboardHealth:
		statement = sessionDashboardHealth
	default:
		return result, fmt.Errorf("invalid session dashboard section %q", section)
	}
	err := db.QueryRow(ctx, statement, args...).
		Scan(&result.Ready, &result.UpdatedAt, &result.PendingSince, &result.Payload)
	return result, err
}
