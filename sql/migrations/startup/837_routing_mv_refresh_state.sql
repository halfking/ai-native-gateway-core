-- Migration 837: stop making every materialized-view refresh a full rewrite.
--
-- Why this exists
-- --------------
-- REFRESH MATERIALIZED VIEW CONCURRENTLY recomputes the aggregate, compares
-- each resulting tuple against the stored one, and only rewrites the rows
-- that actually differ. Both routing analytics views carried
--
--     NOW() AS refreshed_at
--
-- in their target lists. NOW() is volatile and differs on every evaluation,
-- so every tuple differed on every cycle and the "concurrent" refresh
-- degenerated into a full-table rewrite.
--
-- Measured on 252 production (runbook §10.75.6 and §10.75.7):
--   * one refresh inserted 4,415 rows against 4,383 view rows = 100.7%
--   * a read-only control experiment showed only 15 of 4,420 rows (0.339%)
--     had any real difference between consecutive refreshes
--   * REFRESH ... CONCURRENTLY routing_analytics_7d was the 3rd largest WAL
--     producer in the gateway database: 56.33 GB, 8.98% of its WAL, over
--     6,406 refreshes
--
-- Fix
-- ---
-- The refresh timestamp moves out of the view and into a one-row-per-view
-- side table. Consumers (admin.mvFreshWithin) read the stamp from there, and
-- bg.MaterializedViewRefresher writes it after each successful REFRESH.
-- Expected write amplification drops from 100.7% to ~0.34%.
--
-- Both views are dropped and recreated because CREATE MATERIALIZED VIEW
-- cannot change an existing view's column list, and the same rebuild pattern
-- was already proven by migration 649.
--
-- Status: active
-- Idempotent: YES (DROP IF EXISTS + CREATE + UPSERT stamp, safe to replay;
--              each replay rebuilds both views with a fresh 7-day aggregate)
-- Rollback: 837_routing_mv_refresh_state.down.sql
-- Related:  db/db.go routingAnalyticsMVSQL + ensureRoutingAnalyticsMaterializedViews
--           + StampRoutingMVRefreshSQL, bg/materialized_view_refresher.go
--           execRefresh, admin/analytics_materialized.go mvFreshWithin,
--           sql/migrations/startup/up/632_routing_analytics_materialized_view.sql
-- Changelog:
--   2026-10-06  v1.0  Drop NOW() AS refreshed_at from both routing analytics
--                      matviews; add routing_mv_refresh_state as the single
--                      source of refresh time.

\set ON_ERROR_STOP on
BEGIN;

-- The 7-day aggregate can exceed the shared host's statement_timeout; same
-- budget as the Go ensure path (db/db.go sets 10min on the pinned connection)
-- and as migration 649.
SET LOCAL statement_timeout = '10min';

DROP MATERIALIZED VIEW IF EXISTS public.routing_analytics_7d CASCADE;
DROP MATERIALIZED VIEW IF EXISTS public.routing_audit_summary_7d CASCADE;

-- The refresh stamp. One row per view; written by whoever last regenerated
-- the view's content (this migration, or the refresher after a REFRESH).
CREATE TABLE IF NOT EXISTS public.routing_mv_refresh_state (
  view_name TEXT PRIMARY KEY,
  refreshed_at TIMESTAMPTZ NOT NULL
);

COMMENT ON TABLE public.routing_mv_refresh_state IS
  'Last time each routing analytics materialized view was regenerated (migration 837). '
  'Replaces the per-row NOW() AS refreshed_at column that made REFRESH ... CONCURRENTLY '
  'rewrite 100% of every view on every 10-minute cycle.';

-- Identical to migration 649's source view. Kept verbatim so a replay of this
-- file alone converges to the same end state as 649 + 837.
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
  COALESCE(SUM(cost_usd), 0) AS total_cost_usd
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
  COUNT(*) FILTER (WHERE is_auto_request IS NOT TRUE) AS specified_request_count
FROM public.routing_analytics_source
WHERE ts >= NOW() - INTERVAL '7 days'
  AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health', 'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual')
  AND COALESCE(task_type, '') <> 'probe_triggered'
  AND COALESCE(request_id, '') NOT LIKE 'probe-%'
  AND (is_auto_request = TRUE OR (is_auto_request IS NOT TRUE AND client_model IS NOT NULL AND client_model <> ''))
GROUP BY tenant_id;

CREATE UNIQUE INDEX routing_audit_summary_7d_ukey
  ON public.routing_audit_summary_7d (tenant_id);

-- Both views were just rebuilt from scratch, so they ARE fresh right now.
-- This is the one place besides the refresher that may stamp the clock.
INSERT INTO public.routing_mv_refresh_state (view_name, refreshed_at)
VALUES ('routing_analytics_7d', NOW()), ('routing_audit_summary_7d', NOW())
ON CONFLICT (view_name) DO UPDATE SET refreshed_at = EXCLUDED.refreshed_at;

COMMENT ON MATERIALIZED VIEW public.routing_analytics_7d IS
  'Pre-aggregated 7-day routing analytics for /api/admin/auto-route/analytics/* endpoints. '
  'Refreshed every 10 minutes by bg.MaterializedViewRefresher. Refresh time lives in '
  'routing_mv_refresh_state, not in this view (migration 837 — a NOW() column here made '
  'every refresh rewrite 100% of the view). Mirror of db.go routingAnalyticsMVSQL.';

COMMENT ON MATERIALIZED VIEW public.routing_audit_summary_7d IS
  'High-level audit summary for /api/admin/auto-route/audit endpoint. '
  'Refreshed every 10 minutes by bg.MaterializedViewRefresher. Refresh time lives in '
  'routing_mv_refresh_state (migration 837). Mirror of db.go routingAnalyticsMVSQL.';

COMMIT;