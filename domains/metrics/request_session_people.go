package metrics

import (
	"context"
	"fmt"
	"time"
)

type SessionPeopleQuery struct {
	Start, End, PreviousStart time.Time
	Tenant, Owner, Project    string
	Timezone, Section         string
	SortBy, Order             string
	Limit, Offset             int
}

type SessionPeopleResult struct {
	ProjectedAccountActivity
	MatchingTenants int
}

const sessionPeopleBase = `
WITH params AS (
	SELECT $1::timestamptz AS start_at,$2::timestamptz AS end_at,$3::timestamptz AS previous_start,
		$4::text AS tenant,$5::text AS owner,$6::text AS project,$7::text AS zone,
		$8::integer AS page_size,$9::integer AS page_offset,$10::boolean AS rank_peers
), projection AS (
	SELECT s.backfill_complete,s.updated_at,
		(SELECT min(q.enqueued_at) FROM public.request_metrics_pending q
		 WHERE q.occurred_at >= p.start_at AND q.occurred_at < p.end_at
		   AND (p.tenant='' OR q.tenant_id=p.tenant)) AS pending_since
	FROM public.request_metrics_projection_state s CROSS JOIN params p WHERE s.singleton
), owners AS MATERIALIZED (
	SELECT tenant FROM params WHERE owner<>'' AND tenant<>''
	UNION
	SELECT tenant FROM (
		SELECT u.tenant_id AS tenant FROM public.users u CROSS JOIN params p
		WHERE p.owner<>'' AND p.tenant='' AND u.username=p.owner
		UNION
		SELECT m.tenant_id FROM public.request_metrics m CROSS JOIN params p
		WHERE p.owner<>'' AND p.tenant='' AND m.owner_user=p.owner
		  AND m.occurred_at >= p.previous_start AND m.occurred_at < p.end_at
		  AND m.source_fact_id IS NOT NULL AND m.traffic_class='business'
		LIMIT 2
	) candidates
), identity_scope AS (
	SELECT count(*)::integer AS matching_tenants,min(tenant) AS tenant FROM owners
), observed AS MATERIALIZED (
	SELECT m.tenant_id,m.owner_user AS owner,m.occurred_at,
		(m.occurred_at AT TIME ZONE p.zone)::date AS day,
		nullif(m.session_id,'') AS session_id,
		CASE WHEN m.task_id <> 'default' AND m.task_id NOT LIKE 'auto-title:%' THEN nullif(m.task_id,'') END AS task,
		public.gw_resolve_project_ref(d.project_id,coalesce(nullif(d.application_code,''),m.application_code)) AS project,
		m.total_tokens,m.credits_charged,CASE WHEN m.cost_currency='USD' THEN m.cost_amount END AS usd,
		m.status,m.cached_tokens,m.latency_ms,m.api_key_id,m.api_key_prefix,` + requestModelKey + ` AS model
	FROM public.request_metrics m CROSS JOIN params p CROSS JOIN identity_scope i
	LEFT JOIN public.session_dim d ON d.tenant_id=m.tenant_id AND d.gw_session_id=m.session_id
	WHERE m.occurred_at >= p.previous_start AND m.occurred_at < p.end_at
	  AND m.source_fact_id IS NOT NULL AND m.traffic_class='business' AND nullif(m.owner_user,'') IS NOT NULL
	  AND (p.tenant='' OR m.tenant_id=p.tenant)
	  AND (p.owner='' OR (i.matching_tenants=1 AND m.tenant_id=i.tenant AND (p.rank_peers OR m.owner_user=p.owner)))
	  AND (SELECT backfill_complete FROM projection)
), current_rows AS (
	SELECT o.* FROM observed o CROSS JOIN params p
	WHERE o.occurred_at>=p.start_at AND (p.project='' OR o.project=p.project)
)
`

const sessionPeopleMeasures = `
	count(*) AS request_count,
	count(DISTINCT session_id) AS session_count,count(DISTINCT task) AS task_count,
	coalesce(sum(total_tokens),0) AS total_tokens,sum(usd) AS total_cost,
	coalesce(sum(credits_charged),0) AS credits_charged,count(*)-count(usd) AS usd_unmeasured_requests,
	max(occurred_at) AS last_active_at`

const sessionPeopleList = `,
groups AS (
	SELECT tenant_id,owner,count(DISTINCT project) AS project_count,
		(array_agg(task ORDER BY occurred_at DESC,task) FILTER (WHERE task IS NOT NULL))[1] AS latest_task,
		` + sessionPeopleMeasures + `
	FROM current_rows GROUP BY tenant_id,owner
), ranks AS (
	SELECT g.*,row_number() OVER (ORDER BY %s %s NULLS LAST,tenant_id,owner) AS rank FROM groups g
), paged AS (
	SELECT * FROM ranks ORDER BY rank LIMIT (SELECT page_size FROM params) OFFSET (SELECT page_offset FROM params)
), labelled AS (
	SELECT r.*,coalesce(nullif(u.display_name,''),r.owner) AS display_name,coalesce(u.email,'') AS email
	FROM paged r LEFT JOIN public.users u ON u.tenant_id=r.tenant_id AND u.username=r.owner
)
SELECT backfill_complete,updated_at,pending_since,matching_tenants,
	jsonb_build_object('total',(SELECT count(*) FROM groups),
	'users',coalesce((SELECT jsonb_agg(to_jsonb(labelled) ORDER BY rank) FROM labelled),'[]'))
FROM projection CROSS JOIN identity_scope`

const sessionPeopleModels = `,
groups AS (
	SELECT model,count(*) AS request_count,coalesce(sum(total_tokens),0) AS total_tokens,sum(usd) AS total_cost,
		coalesce(sum(credits_charged),0) AS credits_charged,count(*)-count(usd) AS usd_unmeasured_requests,
		count(*) FILTER (WHERE status='success')::float8/count(*) AS success_rate,
		count(*) FILTER (WHERE cached_tokens>0)::float8/count(*) AS cache_hit_rate,
		avg(latency_ms) FILTER (WHERE latency_ms>0) AS avg_response_time_ms,max(occurred_at) AS last_used_at
	FROM current_rows o CROSS JOIN params p WHERE o.owner=p.owner GROUP BY model
)
SELECT backfill_complete,updated_at,pending_since,matching_tenants,
	jsonb_build_object('models',coalesce((SELECT jsonb_agg(to_jsonb(groups)
		ORDER BY request_count DESC,total_cost DESC NULLS LAST,model) FROM groups),'[]'))
FROM projection CROSS JOIN identity_scope`

const sessionPeopleAPIKeys = `,
key_requests AS MATERIALIZED (
	SELECT o.* FROM current_rows o CROSS JOIN params p WHERE o.owner=p.owner AND o.api_key_id IS NOT NULL
), project_usage AS (
	SELECT tenant_id,api_key_id,project,count(*) AS requests
	FROM key_requests WHERE project IS NOT NULL GROUP BY tenant_id,api_key_id,project
), project_totals AS (
	SELECT tenant_id,api_key_id,(array_agg(project ORDER BY requests DESC,project))[1] AS top_project
	FROM project_usage GROUP BY tenant_id,api_key_id
), totals AS (
	SELECT tenant_id,api_key_id,max(api_key_prefix) AS prefix,count(*) AS request_count,
		coalesce(sum(total_tokens),0) AS total_tokens,sum(usd) AS total_cost,
		coalesce(sum(credits_charged),0) AS credits_charged,count(*)-count(usd) AS usd_unmeasured_requests,
		count(DISTINCT project) AS project_count,max(occurred_at) AS last_used_at
	FROM key_requests GROUP BY tenant_id,api_key_id
), labelled AS (
	SELECT r.api_key_id AS key_id,r.request_count,r.total_tokens,r.total_cost,r.credits_charged,
		r.usd_unmeasured_requests,r.project_count,r.last_used_at,coalesce(p.top_project,'') AS top_project,
		coalesce(nullif(k.key_alias,''),nullif(r.prefix,''),'') AS key_alias
	FROM totals r LEFT JOIN project_totals p USING (tenant_id,api_key_id)
	LEFT JOIN public.api_keys k ON k.tenant_id=r.tenant_id AND k.id=r.api_key_id
)
SELECT backfill_complete,updated_at,pending_since,matching_tenants,
	jsonb_build_object('api_keys',coalesce((SELECT jsonb_agg(to_jsonb(labelled)
		ORDER BY request_count DESC,total_cost DESC NULLS LAST,key_id) FROM labelled),'[]'))
FROM projection CROSS JOIN identity_scope`

const sessionPeopleBreakdown = `,
groups AS (
	SELECT %s,` + sessionPeopleMeasures + `
	FROM current_rows o CROSS JOIN params p WHERE o.owner=p.owner AND %s IS NOT NULL GROUP BY %s
), ordered AS (
	SELECT * FROM groups ORDER BY %s %s NULLS LAST,%s
)
SELECT backfill_complete,updated_at,pending_since,matching_tenants,
	jsonb_build_object('%s',coalesce((SELECT jsonb_agg(to_jsonb(ordered)) FROM ordered),'[]'))
FROM projection CROSS JOIN identity_scope`

const sessionPeopleDetail = `,
totals AS (
	SELECT ` + sessionPeopleMeasures + ` FROM current_rows o CROSS JOIN params p WHERE o.owner=p.owner
), owner_totals AS (
	SELECT o.owner,occurred_at>=p.start_at AS current,coalesce(sum(total_tokens),0) AS tokens
	FROM observed o CROSS JOIN params p GROUP BY o.owner,2
), period_rank AS (
	SELECT owner,current,row_number() OVER (PARTITION BY current ORDER BY tokens DESC,owner) AS position FROM owner_totals
), positions AS (
	SELECT max(position) FILTER (WHERE current) AS current_rank,max(position) FILTER (WHERE NOT current) AS prior_rank
	FROM period_rank r CROSS JOIN params p WHERE r.owner=p.owner
), daily_totals AS (
	SELECT o.owner,day,` + sessionPeopleMeasures + `
	FROM current_rows o GROUP BY o.owner,day
), daily_rank AS (
	SELECT *,row_number() OVER (PARTITION BY day ORDER BY total_tokens DESC,owner) AS position FROM daily_totals
), series AS (
	SELECT
		coalesce(jsonb_agg(jsonb_build_object('date',day,'count',session_count) ORDER BY day),'[]') AS sessions,
		coalesce(jsonb_agg(jsonb_build_object('date',day,'count',task_count) ORDER BY day),'[]') AS tasks,
		coalesce(jsonb_agg(jsonb_build_object('date',day,'total_cost_usd',total_cost,'credits_charged',credits_charged) ORDER BY day),'[]') AS cost,
		coalesce(jsonb_agg(jsonb_build_object('date',day,'rank',position) ORDER BY day),'[]') AS rank
	FROM daily_rank d CROSS JOIN params p WHERE d.owner=p.owner
)
SELECT projection.backfill_complete,projection.updated_at,projection.pending_since,matching_tenants,
	jsonb_build_object('user',jsonb_build_object('owner',p.owner,'tenant_id',i.tenant,
		'display_name',coalesce(nullif(u.display_name,''),p.owner),'email',coalesce(u.email,'')),
		'summary',to_jsonb(totals)||jsonb_build_object('rank',current_rank,'rank_scope','tenant','rank_change',prior_rank-current_rank),
		'trends',to_jsonb(series))
FROM projection CROSS JOIN identity_scope i CROSS JOIN params p CROSS JOIN totals CROSS JOIN positions CROSS JOIN series
LEFT JOIN public.users u ON u.tenant_id=i.tenant AND u.username=p.owner`

func ReadSessionPeople(ctx context.Context, db requestStatsDB, query SessionPeopleQuery) (SessionPeopleResult, error) {
	var result SessionPeopleResult
	orderBy := map[string]string{
		"": "session_count", "sessions": "session_count", "tokens": "total_tokens",
		"cost": "total_cost", "last_active": "last_active_at",
	}[query.SortBy]
	if orderBy == "" || (query.Order != "" && query.Order != "asc" && query.Order != "desc") ||
		!query.End.After(query.Start) || query.PreviousStart.After(query.Start) {
		return result, fmt.Errorf("invalid personnel statistics parameters")
	}
	direction := "DESC"
	if query.Order == "asc" {
		direction = "ASC"
	}
	var statement string
	switch query.Section {
	case "identity":
		statement = `SELECT backfill_complete,updated_at,pending_since,matching_tenants,
			jsonb_build_object('tenant_id',tenant) FROM projection CROSS JOIN identity_scope`
	case "ranking":
		if query.SortBy == "" {
			orderBy = "total_tokens"
		}
		statement = fmt.Sprintf(sessionPeopleList, orderBy, direction)
	case "detail":
		statement = sessionPeopleDetail
	case "models":
		statement = sessionPeopleModels
	case "api_keys":
		statement = sessionPeopleAPIKeys
	case "projects", "tasks":
		key := "o.project"
		if query.Section == "tasks" {
			key = "o.task"
		}
		statement = fmt.Sprintf(sessionPeopleBreakdown, key, key, key, orderBy, direction, key[2:], query.Section)
	default:
		return result, fmt.Errorf("unknown personnel statistics section")
	}
	err := db.QueryRow(ctx, sessionPeopleBase+statement, query.Start, query.End, query.PreviousStart,
		query.Tenant, query.Owner, query.Project, query.Timezone, query.Limit, query.Offset, query.Section == "detail").
		Scan(&result.Ready, &result.UpdatedAt, &result.PendingSince, &result.MatchingTenants, &result.Payload)
	return result, err
}
