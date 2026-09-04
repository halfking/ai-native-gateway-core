-- Migration 649: exclude self-check/probe traffic from routing analytics.
-- Existing migration 632 views keep their original SELECT definition when
-- refreshed, so replace both views once before the refresher resumes.
-- Idempotent: dropping and recreating the derived views is safe; source logs
-- are never modified.

\set ON_ERROR_STOP on
BEGIN;

DROP MATERIALIZED VIEW IF EXISTS public.routing_analytics_7d CASCADE;
DROP MATERIALIZED VIEW IF EXISTS public.routing_audit_summary_7d CASCADE;

-- Keep the historical request-log wrappers untouched. Their frozen column
-- contracts include columns and casts that are not present in both base tables.
-- Analytics gets its own narrow, stable source view instead.
DROP VIEW IF EXISTS public.routing_analytics_source;

CREATE VIEW public.routing_analytics_source AS
SELECT
  ts,
  task_type::text AS task_type,
  outbound_model::text AS outbound_model,
  client_model::text AS client_model,
  work_type::text AS work_type,
  provider_id::bigint AS provider_id,
  credential_id::bigint AS credential_id,
  is_auto_request::boolean AS is_auto_request,
  tenant_id::text AS tenant_id,
  request_id::text AS request_id,
  success::boolean AS success,
  latency_ms::numeric AS latency_ms,
  cost_usd::numeric AS cost_usd,
  origin_stage::text AS origin_stage
FROM public.request_logs_hot
UNION ALL
SELECT
  ts,
  task_type::text AS task_type,
  outbound_model::text AS outbound_model,
  client_model::text AS client_model,
  work_type::text AS work_type,
  provider_id::bigint AS provider_id,
  credential_id::bigint AS credential_id,
  is_auto_request::boolean AS is_auto_request,
  tenant_id::text AS tenant_id,
  request_id::text AS request_id,
  success::boolean AS success,
  latency_ms::numeric AS latency_ms,
  cost_usd::numeric AS cost_usd,
  origin_stage::text AS origin_stage
FROM public.request_logs;

CREATE MATERIALIZED VIEW public.routing_analytics_7d AS
SELECT
  DATE_TRUNC('hour', ts) AS time_bucket,
  COALESCE(NULLIF(task_type, ''), CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END) AS effective_task_type,
  COALESCE(NULLIF(outbound_model, ''), client_model) AS effective_model,
  COALESCE(NULLIF(work_type, ''), 'unknown') AS effective_work_type,
  COALESCE(provider_id, (SELECT cr.provider_id FROM credentials cr WHERE cr.id = credential_id LIMIT 1)) AS effective_provider_id,
  COALESCE(is_auto_request, FALSE) AS is_auto_request,
  tenant_id,
  COUNT(*) AS request_count,
  COUNT(*) FILTER (WHERE success) AS success_count,
  COUNT(*) FILTER (WHERE is_auto_request = TRUE) AS auto_request_count,
  COUNT(*) FILTER (WHERE is_auto_request IS NOT TRUE) AS specified_request_count,
  percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms) AS p50_latency_ms,
  percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) AS p95_latency_ms,
  percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ms) AS p99_latency_ms,
  COALESCE(SUM(cost_usd), 0) AS total_cost_usd,
  NOW() AS refreshed_at
FROM public.routing_analytics_source
WHERE ts >= NOW() - INTERVAL '7 days'
  AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health', 'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual')
  AND COALESCE(task_type, '') <> 'probe_triggered'
  AND COALESCE(request_id, '') NOT LIKE 'probe-%'
  AND (is_auto_request = TRUE OR (is_auto_request IS NOT TRUE AND client_model IS NOT NULL AND client_model <> ''))
  AND COALESCE(NULLIF(outbound_model, ''), client_model) IS NOT NULL
GROUP BY time_bucket, effective_task_type, effective_model, effective_work_type,
         effective_provider_id, is_auto_request, tenant_id;

CREATE UNIQUE INDEX routing_analytics_7d_ukey
  ON public.routing_analytics_7d (time_bucket, effective_task_type, effective_model,
                                  effective_work_type, effective_provider_id,
                                  is_auto_request, tenant_id);
CREATE INDEX routing_analytics_7d_task_model_idx
  ON public.routing_analytics_7d (effective_task_type, effective_model);
CREATE INDEX routing_analytics_7d_time_idx
  ON public.routing_analytics_7d (time_bucket DESC);
CREATE INDEX routing_analytics_7d_tenant_idx
  ON public.routing_analytics_7d (tenant_id) WHERE tenant_id IS NOT NULL;

CREATE MATERIALIZED VIEW public.routing_audit_summary_7d AS
SELECT
  tenant_id,
  COUNT(*) AS total_requests,
  COUNT(*) FILTER (WHERE success) AS success_count,
  COUNT(*) FILTER (WHERE is_auto_request = TRUE) AS auto_request_count,
  COUNT(*) FILTER (WHERE is_auto_request IS NOT TRUE) AS specified_request_count,
  NOW() AS refreshed_at
FROM public.routing_analytics_source
WHERE ts >= NOW() - INTERVAL '7 days'
  AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health', 'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual')
  AND COALESCE(task_type, '') <> 'probe_triggered'
  AND COALESCE(request_id, '') NOT LIKE 'probe-%'
  AND (is_auto_request = TRUE OR (is_auto_request IS NOT TRUE AND client_model IS NOT NULL AND client_model <> ''))
GROUP BY tenant_id;

CREATE UNIQUE INDEX routing_audit_summary_7d_ukey
  ON public.routing_audit_summary_7d (tenant_id);

COMMIT;
