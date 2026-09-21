-- Migration 632: Create materialized views for routing analytics performance optimization
--
-- Purpose: Pre-aggregate hot query data for /api/admin/auto-route/analytics/* endpoints
--          (flow, matrix, audit) instead of aggregating 314K+ rows per request.
--          Refreshed every 10 minutes by bg.MaterializedViewRefresher
--          (REFRESH ... CONCURRENTLY + advisory lock).
--
-- ============================================================================
-- REWRITTEN 2026-09-05 — this file is now replayable and matches db.go.
-- ============================================================================
-- The previous version of this file aggregated directly from the frozen
-- request-log wrapper view `request_logs_with_current_month_without_customer_id`.
-- That wrapper never gained the `origin_stage` probe-origin column, so replaying
-- this file failed with SQLSTATE 42703 on any current database (observed
-- 2026-09-04) and the file could never have reproduced the live behaviour.
--
-- Actual behaviour is owned by db.ensureRoutingAnalyticsMaterializedViews
-- (db/db.go) which builds both matviews from the narrow source view
-- `routing_analytics_source` (request_logs_hot UNION ALL request_logs) via the
-- shared `routingAnalyticsMVSQL` constant. This file now mirrors exactly that
-- definition, so a direct replay reaches the same end state as the Go ensure
-- path. Keep all three in sync: db/db.go, this file, and
-- sql/migrations/startup/649_routing_analytics_probe_filter.sql (649 is the
-- same rebuild shipped as the probe-filter migration; db.go's stale-definition
-- gate rebuilds automatically whenever `origin_stage` is missing from any of
-- the three view definitions).
--
-- Historical design notes carried over from the original 632:
--   - NULL-safety: is_auto_request is COALESCEd to FALSE in the view. GROUP BY
--     keeps NULL and FALSE in separate buckets while the unique index maps both
--     onto the same COALESCE key, which would make CREATE UNIQUE INDEX fail
--     with a duplicate key and abort startup.
--   - Tenant sentinel: tenant_id is TEXT; unique indexes use plain columns
--     (expression indexes are rejected by REFRESH ... CONCURRENTLY, SQLSTATE
--     55000 on prod PG17). GROUP BY collapses NULLs into one row per key, so
--     plain-column uniqueness is safe.
--   - effective_provider_id bakes in the COALESCE(provider_id, credential
--     lookup) fallback from buildFlowL23Query (admin/analytics.go) so the
--     L2→L3 Sankey reports the same 'unknown' provider share on materialized
--     and base paths.
--   - Legacy expression `_pkey` indexes (original 632 deploy) die together
--     with the matviews dropped below; db.go keeps explicit
--     `DROP INDEX IF EXISTS …_pkey` statements only because its no-drop
--     index-repair path recreates the matviews in place.
--
-- Status: active (mirror of db.go; runtime behaviour owned by db.go)
-- Idempotent: YES (DROP IF EXISTS + CREATE, safe to replay; rebuilds both
--              matviews with a fresh 7-day aggregate on each run)
-- Rollback: down/632_routing_analytics_materialized_view.down.sql
-- Related:  db/db.go routingAnalyticsMVSQL + ensureRoutingAnalyticsMaterializedViews,
--           bg/materialized_view_refresher.go, admin/analytics_materialized.go
-- Changelog:
--   2026-08-31  v1.0  Initial materialized view creation
--   2026-08-31  v1.1  Audit fixes: NULL-safe is_auto_request, provider
--                     credential fallback, plain-column unique index
--   2026-09-01  v1.2  Incident fix: unique-index tenant sentinel -1 → ''
--                     (SQLSTATE 42804 on text tenant_id)
--   2026-09-01  v1.3  Swap expression unique indexes (_pkey) for plain-column
--                     _ukey (REFRESH ... CONCURRENTLY rejects expression
--                     indexes, SQLSTATE 55000)
--   2026-09-05  v2.0  REWRITE: read from routing_analytics_source
--                     (hot UNION ALL parent) instead of the frozen
--                     request-log wrapper; aligns byte-for-byte semantics
--                     with db.go routingAnalyticsMVSQL / migration 649 so
--                     direct replay no longer fails on missing
--                     wrapper origin_stage (SQLSTATE 42703, seen 2026-09-04)

\set ON_ERROR_STOP on
BEGIN;

-- The 7-day aggregate can exceed a shared host's 30s statement_timeout; same
-- budget as the Go ensure path (db/db.go sets 10min on the pinned connection).
SET LOCAL statement_timeout = '10min';

DROP MATERIALIZED VIEW IF EXISTS public.routing_analytics_7d CASCADE;
DROP MATERIALIZED VIEW IF EXISTS public.routing_audit_summary_7d CASCADE;

-- Keep the historical request-log wrappers untouched. Their frozen column
-- contracts include columns and casts that are not present in both base
-- tables. Analytics gets its own narrow, stable source view instead.
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
  origin_stage::text AS origin_stage,
  auto_profile::text AS auto_profile
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
  origin_stage::text AS origin_stage,
  auto_profile::text AS auto_profile
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

-- Plain-column unique index: required for REFRESH ... CONCURRENTLY (PG
-- rejects expression indexes, SQLSTATE 55000). See header notes.
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

COMMENT ON MATERIALIZED VIEW public.routing_analytics_7d IS
  'Pre-aggregated 7-day routing analytics for /api/admin/auto-route/analytics/* endpoints. '
  'Refreshed every 10 minutes by bg.MaterializedViewRefresher. '
  'Mirror of db.go routingAnalyticsMVSQL (migration 632, rewritten 2026-09-05).';

COMMENT ON MATERIALIZED VIEW public.routing_audit_summary_7d IS
  'High-level audit summary for /api/admin/auto-route/audit endpoint. '
  'Refreshed every 10 minutes by bg.MaterializedViewRefresher. '
  'Mirror of db.go routingAnalyticsMVSQL (migration 632, rewritten 2026-09-05).';

COMMIT;
