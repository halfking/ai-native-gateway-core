-- Migration 722: Expand routing_analytics_source with origin_actor column
--
-- Renumber history: first cut was numbered 720 (2026-09-17) but collided with
-- the installer-only 720_rls_policy_vocabulary_unification migration
-- (f5328e13c) in installer embeddata; startup versions >=492 must be globally
-- unique (TestNumericUpMigrationVersionsAreUnique), so this migration moved
-- to 722 on 2026-09-18 before ever being deployed or ledgered.
--
-- Purpose: Project origin_actor into routing_analytics_source so the MV layer
--          can filter synthetic actors (goal-%, internal loopbacks) directly
--          instead of relying only on origin_stage inference. Completes the
--          synthetic-actor filtering architecture started in bg/auto_route_settle
--          (R37-R2) and request_logs_hot.origin_actor (migration 717).
--
-- Context (R37 §四#2, R38): routing_analytics_source (migration 632) projects
--          origin_stage for probe filtering but not origin_actor. Admin KPI
--          matviews (routing_analytics_7d / routing_audit_summary_7d) therefore
--          cannot exclude goal-audit shadow rounds or other synthetic traffic
--          at the MV layer — they filter only by origin_stage + request_id prefix.
--          Adding origin_actor enables future WHERE clauses such as
--          `COALESCE(origin_actor, '') NOT LIKE 'goal-%'` without rebuilding
--          the source view again.
--
-- Design: CREATE OR REPLACE VIEW can only APPEND columns (never reorder or
--         remove), so origin_actor becomes the new LAST column in both UNION
--         branches. The two dependent matviews (routing_analytics_7d /
--         routing_audit_summary_7d) must be rebuilt (DROP + CREATE) because
--         they pin the view's column list at creation time; a plain ALTER VIEW
--         leaves stale matviews selecting the old 15-column projection.
--
--         This migration does NOT change the matviews' WHERE clauses or GROUP BY
--         keys; it only makes origin_actor available for future filter evolution.
--         The current probe-filtering logic (origin_stage NOT IN + task_type <>
--         + request_id NOT LIKE) stays intact.
--
-- Rollback: 722_routing_analytics_add_origin_actor.down.sql (removes
--           origin_actor from the source view + rebuilds matviews with the
--           15-column projection)
--
-- Installer sync: This migration follows the five-point synchronization
--                 convention (R37 §conventions):
--                   1. sql/migrations/startup/722_*.sql (this file)
--                   2. sql/migrations/startup/722_*.down.sql
--                   3. installer/cmd/llm-gw-installer/embeddata/startup/722_*.sql
--                   4. installer/cmd/llm-gw-installer/embeddata/startup/722_*.down.sql
--                   5. installer gate: dbinit.Runner.StartupFiles +
--                      go:embed var / embeddedSQLFiles map in main.go
--                   6. db.go ensure path: db/db.go routingAnalyticsMVSQL
--                      constant (16-column projection, kept in lockstep)
--
-- Status: active (R38 authoring 2026-09-17, renumbered + wired 2026-09-18)
-- Idempotent: YES (CREATE OR REPLACE VIEW + DROP/CREATE matviews)
-- Locks: VIEW rebuild takes ACCESS EXCLUSIVE on routing_analytics_source
--        (momentary metadata lock; no row lock). Matview DROP + CREATE
--        populates ~314K 7-day rows (observed ~5-8s on prod workload);
--        low-peak execution recommended.
-- Related: sql/migrations/startup/up/632_routing_analytics_materialized_view.sql,
--          sql/migrations/startup/649_routing_analytics_probe_filter.sql,
--          db/db.go routingAnalyticsMVSQL

\set ON_ERROR_STOP on
BEGIN;

-- Same timeout budget as migration 632/649 and db.go ensure path: the 7-day
-- aggregate can exceed shared host's default 30s statement_timeout.
SET LOCAL statement_timeout = '10min';

-- Step 1: Expand routing_analytics_source with origin_actor as the new LAST column.
--         CREATE OR REPLACE VIEW appends; existing matviews still see the old
--         15-column projection until rebuilt.
CREATE OR REPLACE VIEW public.routing_analytics_source AS
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
  auto_profile::text AS auto_profile,
  origin_actor::text AS origin_actor
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
  auto_profile::text AS auto_profile,
  origin_actor::text AS origin_actor
FROM public.request_logs;

-- Step 2: Rebuild routing_analytics_7d to consume the 16-column projection.
--         The WHERE clause and GROUP BY stay unchanged; origin_actor is
--         available but not yet used in filters (future evolution).
DROP MATERIALIZED VIEW IF EXISTS public.routing_analytics_7d CASCADE;

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

-- Step 3: Rebuild routing_audit_summary_7d to consume the 16-column projection.
DROP MATERIALIZED VIEW IF EXISTS public.routing_audit_summary_7d CASCADE;

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

COMMENT ON VIEW public.routing_analytics_source IS
  'Narrow analytics source view (request_logs_hot UNION ALL request_logs). '
  'Isolates KPI matviews from the frozen request-log wrapper column contract. '
  'Migration 722 (renumbered from 720, 2026-09-18) added origin_actor for synthetic-actor filtering.';

COMMENT ON MATERIALIZED VIEW public.routing_analytics_7d IS
  'Pre-aggregated 7-day routing analytics for /api/admin/auto-route/analytics/* endpoints. '
  'Refreshed every 10 minutes by bg.MaterializedViewRefresher. '
  'Rebuilt by migration 722 (renumbered from 720, 2026-09-18) to consume origin_actor projection.';

COMMENT ON MATERIALIZED VIEW public.routing_audit_summary_7d IS
  'High-level audit summary for /api/admin/auto-route/audit endpoint. '
  'Refreshed every 10 minutes by bg.MaterializedViewRefresher. '
  'Rebuilt by migration 722 (renumbered from 720, 2026-09-18) to consume origin_actor projection.';

COMMIT;
