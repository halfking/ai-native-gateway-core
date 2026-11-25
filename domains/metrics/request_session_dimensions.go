package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type SessionDimensionQuery struct {
	Start, End, ActiveSince time.Time
	Tenant, Timezone        string
	Dimension, ID, Order    string
	Limit, Offset           int
	IncludeProbes           bool
}

const sessionDimensionBase = `
WITH projection AS (
	SELECT backfill_complete,updated_at,
		(SELECT min(enqueued_at) FROM public.request_metrics_pending
		 WHERE occurred_at >= $1 AND occurred_at < $2 AND ($3='' OR tenant_id=$3)) AS pending_since
	FROM public.request_metrics_projection_state WHERE singleton
), observed AS MATERIALIZED (
	SELECT m.tenant_id,nullif(m.session_id,'') AS session_id,m.occurred_at,
		(m.occurred_at AT TIME ZONE $4)::date AS day,
		d.client_id,d.task_id,d.%PRIMARY% AS dimension_id,d.%SECONDARY% AS related_id,m.person_key,
		` + requestModelKey + ` AS model,m.status,m.latency_ms,m.credits_charged,m.total_tokens,
		CASE WHEN m.cost_currency='USD' THEN m.cost_amount END AS usd
	FROM public.request_metrics_projection m
	CROSS JOIN LATERAL (
		SELECT nullif(m.client_id,'') AS client_id,nullif(m.owner_user,'') AS owner_user,
			CASE WHEN m.task_id <> 'default' AND m.task_id NOT LIKE 'auto-title:%' THEN nullif(m.task_id,'') END AS task_id
	) d
	WHERE m.occurred_at >= $1 AND m.occurred_at < $2 AND ($3='' OR m.tenant_id=$3)
	  AND ($5='' OR d.%PRIMARY%=$5) AND d.%PRIMARY% IS NOT NULL
	  AND ($7::boolean OR m.traffic_class='business') AND m.source_fact_id IS NOT NULL
	  AND (SELECT backfill_complete FROM projection)
), session_usage AS (
	SELECT dimension_id,tenant_id,session_id,count(*) AS request_count,sum(usd) AS cost_usd,
		max(occurred_at) AS last_activity
	FROM observed WHERE session_id IS NOT NULL GROUP BY dimension_id,tenant_id,session_id
), session_rows AS MATERIALIZED (
	SELECT r.*,d.first_request_at AS created_at,s.health_score,s.health_grade
	FROM session_usage r
	LEFT JOIN public.session_dim d ON d.tenant_id=r.tenant_id AND d.gw_session_id=r.session_id
	LEFT JOIN public.session_summaries s ON s.tenant_id=r.tenant_id AND s.session_key=r.session_id
), sessions AS (
	SELECT dimension_id,count(*) AS session_count,count(*) FILTER (WHERE last_activity >= $6) AS active_sessions_24h,
		avg(cost_usd) AS avg_cost_per_session,round(avg(health_score))::int AS avg_health_score,
		jsonb_build_object('a',count(*) FILTER (WHERE health_grade='A'),'b',count(*) FILTER (WHERE health_grade='B'),
			'c',count(*) FILTER (WHERE health_grade='C'),'d',count(*) FILTER (WHERE health_grade='D'),
			'f',count(*) FILTER (WHERE health_grade='F'),'not_assessed',
			count(*) FILTER (WHERE health_grade IS NULL OR health_grade NOT IN ('A','B','C','D','F'))) AS health_distribution
	FROM session_rows GROUP BY dimension_id
), requests AS (
	SELECT dimension_id,count(*) AS total_requests,
		count(*) FILTER (WHERE status='success') AS total_success,count(*) FILTER (WHERE status<>'success') AS total_errors,
		sum(usd) AS total_cost_usd,count(*)-count(usd) AS usd_unmeasured_requests,
		count(DISTINCT (tenant_id,person_key)) FILTER (WHERE nullif(person_key,'') IS NOT NULL) AS end_user_count,
		coalesce(sum(credits_charged),0) AS credits_charged,coalesce(sum(total_tokens),0) AS total_tokens,
		round(avg(latency_ms) FILTER (WHERE latency_ms>0))::int AS avg_latency_ms,
		min(occurred_at) AS first_seen_at,max(occurred_at) AS last_seen_at,
		coalesce(to_jsonb(array_agg(DISTINCT model ORDER BY model) FILTER (WHERE model<>'__unknown__')),'[]') AS models_used,
		coalesce(to_jsonb(array_agg(DISTINCT client_id ORDER BY client_id) FILTER (WHERE client_id IS NOT NULL)),'[]') AS clients_used
	FROM observed GROUP BY dimension_id
), summary AS (
	SELECT r.dimension_id AS %PRIMARY%,r.total_requests,r.total_success,r.total_errors,r.total_cost_usd,r.end_user_count,
		r.usd_unmeasured_requests,r.credits_charged,r.total_tokens,r.avg_latency_ms,r.first_seen_at,r.last_seen_at,
		r.models_used,r.clients_used,coalesce(s.session_count,0) AS session_count,
		coalesce(s.active_sessions_24h,0) AS active_sessions_24h,s.avg_cost_per_session,s.avg_health_score,
		coalesce(s.health_distribution,'{"a":0,"b":0,"c":0,"d":0,"f":0,"not_assessed":0}') AS health_distribution
	FROM requests r LEFT JOIN sessions s USING (dimension_id)
)
`

const sessionDimensionList = `,
paged AS (
	SELECT * FROM summary ORDER BY %ORDER% DESC NULLS LAST,%PRIMARY% LIMIT $8::int OFFSET $9::int
)
SELECT backfill_complete,updated_at,pending_since,
	jsonb_build_object('total',(SELECT count(*) FROM summary),'limit',$8::int,'offset',$9::int,
		'%COLLECTION%',coalesce((SELECT jsonb_agg(to_jsonb(paged) ORDER BY %ORDER% DESC NULLS LAST,%PRIMARY%) FROM paged),'[]'))
FROM projection`

const sessionDimensionDetail = `,
daily AS (
	SELECT day AS date,sum(usd) AS cost,count(DISTINCT (tenant_id,session_id))
		FILTER (WHERE session_id IS NOT NULL) AS sessions,count(*) AS requests
	FROM observed GROUP BY day
), recent AS (
	SELECT tenant_id,session_id,request_count,cost_usd,health_score,health_grade,created_at,last_activity
	FROM session_rows ORDER BY last_activity DESC,tenant_id,session_id LIMIT 20
), related_requests AS (
	SELECT related_id AS %SECONDARY%,count(DISTINCT (tenant_id,session_id))
		FILTER (WHERE session_id IS NOT NULL) AS session_count,sum(usd) AS total_cost_usd,max(occurred_at) AS last_activity
	FROM observed WHERE related_id IS NOT NULL GROUP BY related_id
), related_sessions AS (
	SELECT DISTINCT related_id,tenant_id,session_id FROM observed WHERE related_id IS NOT NULL AND session_id IS NOT NULL
), related_health AS (
	SELECT r.related_id AS %SECONDARY%,round(avg(s.health_score))::int AS avg_health
	FROM related_sessions r JOIN session_rows s USING (tenant_id,session_id) GROUP BY r.related_id
), related AS (
	SELECT r.*,r.total_cost_usd AS total_cost,h.avg_health FROM related_requests r LEFT JOIN related_health h USING (%SECONDARY%)
	ORDER BY total_cost_usd DESC NULLS LAST,%SECONDARY% LIMIT 10
)
%PROFILE_CTES%
SELECT backfill_complete,updated_at,pending_since,
	(SELECT to_jsonb(summary)||jsonb_build_object(
		'daily_cost_trend',coalesce((SELECT jsonb_agg(to_jsonb(daily) ORDER BY date) FROM daily),'[]'),
		'recent_sessions',coalesce((SELECT jsonb_agg(to_jsonb(recent) ORDER BY last_activity DESC,tenant_id,session_id) FROM recent),'[]'),
		'%RELATED%',coalesce((SELECT jsonb_agg(to_jsonb(related) ORDER BY total_cost_usd DESC NULLS LAST,%SECONDARY%) FROM related),'[]')
		%PROFILE_OUTPUT%
	) FROM summary)
FROM projection`

const sessionProfileEndUsers = `,
	end_user_requests AS (
		SELECT person_key AS end_user_id,count(DISTINCT (tenant_id,session_id))
			FILTER (WHERE session_id IS NOT NULL) AS session_count,
			sum(usd) AS total_cost_usd,max(occurred_at) AS last_activity
		FROM observed WHERE nullif(person_key,'') IS NOT NULL GROUP BY person_key
	), end_user_sessions AS (
		SELECT DISTINCT person_key AS end_user_id,tenant_id,session_id FROM observed
		WHERE nullif(person_key,'') IS NOT NULL AND session_id IS NOT NULL
	), end_user_health AS (
		SELECT r.end_user_id,round(avg(s.health_score))::int AS avg_health
		FROM end_user_sessions r JOIN session_rows s USING (tenant_id,session_id) GROUP BY r.end_user_id
	), top_end_users AS (
		SELECT r.*,h.avg_health FROM end_user_requests r LEFT JOIN end_user_health h USING (end_user_id)
		ORDER BY total_cost_usd DESC NULLS LAST,end_user_id LIMIT 10
	)`

func ReadSessionDimensions(ctx context.Context, db requestStatsDB, query SessionDimensionQuery) (ProjectedAccountActivity, error) {
	var result ProjectedAccountActivity
	if !query.End.After(query.Start) {
		return result, fmt.Errorf("invalid session dimension window")
	}
	var primary, secondary, related, order, profileCTEs, profileOutput string
	switch query.Dimension {
	case "clients":
		primary, secondary, related, order = "client_id", "task_id", "related_tasks", "total_cost_usd"
	case "tasks":
		primary, secondary, related, order = "task_id", "client_id", "related_clients", "session_count"
	case "owners":
		if query.ID == "" || query.Tenant == "" {
			return result, fmt.Errorf("owner profile requires a resolved tenant and username")
		}
		primary, secondary, related, order = "owner_user", "task_id", "top_tasks", "total_cost_usd"
		profileCTEs = sessionProfileEndUsers
		profileOutput = `,'top_end_users',coalesce((SELECT jsonb_agg(to_jsonb(top_end_users)
			ORDER BY total_cost_usd DESC NULLS LAST,end_user_id) FROM top_end_users),'[]')`
	default:
		return result, fmt.Errorf("invalid session dimension")
	}
	switch query.Order {
	case "cost":
		order = "total_cost_usd"
	case "sessions":
		order = "session_count"
	case "health":
		order = "avg_health_score"
	}
	args := []any{query.Start, query.End, query.Tenant, query.Timezone, query.ID, query.ActiveSince, query.IncludeProbes}
	statement := sessionDimensionBase
	if query.ID == "" {
		statement += sessionDimensionList
		args = append(args, query.Limit, query.Offset)
	} else {
		statement += sessionDimensionDetail
	}
	statement = strings.NewReplacer("%PRIMARY%", primary, "%SECONDARY%", secondary, "%RELATED%", related,
		"%ORDER%", order, "%COLLECTION%", query.Dimension,
		"%PROFILE_CTES%", profileCTEs, "%PROFILE_OUTPUT%", profileOutput).Replace(statement)
	err := db.QueryRow(ctx, statement, args...).Scan(&result.Ready, &result.UpdatedAt, &result.PendingSince, &result.Payload)
	return result, err
}
