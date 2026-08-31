-- Migration 632: Create materialized views for routing analytics performance optimization
--
-- Purpose: Pre-aggregate hot query data for /api/admin/auto-route/analytics/* endpoints
--          to resolve timeout issues with request_logs_with_current_month_without_customer_id.
--
-- Root cause:
--   Analytics endpoints (flow, matrix, audit) aggregate 314K+ rows on every
--   request, causing 15s timeouts. No index can fix GROUP BY cost at this scale.
--
-- Solution:
--   Materialized views for the 7d window, refreshed every 10 minutes by
--   bg.MaterializedViewRefresher (REFRESH ... CONCURRENTLY + advisory lock).
--
-- IMPORTANT — how this migration actually runs:
--   The startup migration engine is Go-driven (db.applyMigrationsOnce);
--   this file is the DBA-facing mirror of db.ensureRoutingAnalyticsMaterializedViews.
--   Editing only this file does NOT change database behaviour; keep both in sync.
--
-- NULL-safety (2026-08-31 audit): is_auto_request is COALESCEd to FALSE in the
-- view. GROUP BY keeps NULL and FALSE in separate buckets while the unique
-- index maps both onto the same COALESCE key, which would make CREATE UNIQUE
-- INDEX fail with a duplicate key and abort startup. Normalizing here also
-- matches the base queries (`is_auto_request IS NOT TRUE` reads NULL as FALSE).
--
-- effective_provider_id bakes in the COALESCE(provider_id, credential lookup)
-- fallback from buildFlowL23Query (admin/analytics.go) so the L2→L3 Sankey
-- reports the same 'unknown' provider share on materialized and base paths.
--
-- Status: active
-- Idempotent: YES (IF NOT EXISTS throughout)
-- Rollback: down/632_routing_analytics_materialized_view.down.sql
-- Related: db/db.go ensureRoutingAnalyticsMaterializedViews, bg/materialized_view_refresher.go,
--          admin/analytics_materialized.go
-- Changelog:
--   2026-08-31  v1.0  Initial materialized view creation
--   2026-08-31  v1.1  Audit fixes: NULL-safe is_auto_request, provider
--                     credential fallback, plain-column unique index
--
-- Performance target:
--   Query latency: 15s → <500ms
--   Refresh cost: ~5-10s per refresh (every 10 minutes)

BEGIN;

-- =============================================================================
-- 1. Primary materialized view: 7-day window analytics
-- =============================================================================
-- Aggregates all dimensions needed by matrix, flow, and audit endpoints.
-- Covers both auto-routing requests and explicit-model requests.

CREATE MATERIALIZED VIEW IF NOT EXISTS routing_analytics_7d AS
SELECT
  -- Time dimension (hourly buckets for granular drill-down)
  DATE_TRUNC('hour', ts) AS time_bucket,

  -- Task dimension (L1 classification or __specified__ synthetic key)
  COALESCE(
    NULLIF(task_type, ''),
    CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END
  ) AS effective_task_type,

  -- Model dimension (outbound for auto, client for explicit)
  COALESCE(NULLIF(outbound_model, ''), client_model) AS effective_model,

  -- Work type dimension (for row=work_type matrix queries)
  COALESCE(NULLIF(work_type, ''), 'unknown') AS effective_work_type,

  -- Provider dimension (for L2→L3 flow); credential fallback keeps parity
  -- with buildFlowL23Query's COALESCE(rl.provider_id, credential lookup).
  COALESCE(
    provider_id,
    (SELECT cr.provider_id FROM credentials cr WHERE cr.id = credential_id LIMIT 1)
  ) AS effective_provider_id,

  -- Request classification, NULL-normalized (see header note)
  COALESCE(is_auto_request, FALSE) AS is_auto_request,

  -- Tenant scope (for multi-tenant filtering)
  tenant_id,

  -- Aggregated metrics
  COUNT(*) AS request_count,
  COUNT(*) FILTER (WHERE success) AS success_count,
  COUNT(*) FILTER (WHERE is_auto_request = TRUE) AS auto_request_count,
  COUNT(*) FILTER (WHERE is_auto_request IS NOT TRUE) AS specified_request_count,

  -- Latency metrics (percentiles)
  percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms) AS p50_latency_ms,
  percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) AS p95_latency_ms,
  percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ms) AS p99_latency_ms,

  -- Cost metrics
  COALESCE(SUM(cost_usd), 0) AS total_cost_usd,

  -- Refresh metadata
  NOW() AS refreshed_at

FROM request_logs_with_current_month_without_customer_id

WHERE ts >= NOW() - INTERVAL '7 days'
  AND (
    -- Include auto-routing requests
    is_auto_request = TRUE
    -- Include explicit-model requests (historical NULL treated as non-auto)
    OR (is_auto_request IS NOT TRUE AND client_model IS NOT NULL AND client_model <> '')
  )
  -- Filter out rows with no usable model identifier
  AND COALESCE(NULLIF(outbound_model, ''), client_model) IS NOT NULL

GROUP BY
  time_bucket,
  effective_task_type,
  effective_model,
  effective_work_type,
  effective_provider_id,
  is_auto_request,
  tenant_id;

-- Unique index for CONCURRENTLY refresh support. is_auto_request needs no
-- COALESCE here because the view normalizes it; the remaining nullable
-- keys (provider, tenant) are coalesced so NULLs cannot collide.
CREATE UNIQUE INDEX IF NOT EXISTS routing_analytics_7d_pkey
  ON routing_analytics_7d (
    time_bucket,
    effective_task_type,
    effective_model,
    effective_work_type,
    COALESCE(effective_provider_id, -1),
    is_auto_request,
    COALESCE(tenant_id, -1)
  );

-- Covering indexes for common query patterns
CREATE INDEX IF NOT EXISTS routing_analytics_7d_task_model_idx
  ON routing_analytics_7d (effective_task_type, effective_model);

CREATE INDEX IF NOT EXISTS routing_analytics_7d_time_idx
  ON routing_analytics_7d (time_bucket DESC);

CREATE INDEX IF NOT EXISTS routing_analytics_7d_tenant_idx
  ON routing_analytics_7d (tenant_id)
  WHERE tenant_id IS NOT NULL;

COMMENT ON MATERIALIZED VIEW routing_analytics_7d IS
  'Pre-aggregated 7-day routing analytics for /api/admin/auto-route/analytics/* endpoints. '
  'Refreshed every 10 minutes by bg.MaterializedViewRefresher. '
  'Created by migration 632 (2026-08-31).';

-- =============================================================================
-- 2. Audit summary view: simplified aggregates for /audit endpoint
-- =============================================================================
-- The audit endpoint needs only high-level counts, not per-task/model breakdown.

CREATE MATERIALIZED VIEW IF NOT EXISTS routing_audit_summary_7d AS
SELECT
  -- Tenant scope
  tenant_id,

  -- High-level counts
  COUNT(*) AS total_requests,
  COUNT(*) FILTER (WHERE success) AS success_count,
  COUNT(*) FILTER (WHERE is_auto_request = TRUE) AS auto_request_count,
  COUNT(*) FILTER (WHERE is_auto_request IS NOT TRUE) AS specified_request_count,

  -- Refresh metadata
  NOW() AS refreshed_at

FROM request_logs_with_current_month_without_customer_id

WHERE ts >= NOW() - INTERVAL '7 days'
  AND (
    is_auto_request = TRUE
    OR (is_auto_request IS NOT TRUE AND client_model IS NOT NULL AND client_model <> '')
  )

GROUP BY tenant_id;

CREATE UNIQUE INDEX IF NOT EXISTS routing_audit_summary_7d_pkey
  ON routing_audit_summary_7d (COALESCE(tenant_id, -1));

COMMENT ON MATERIALIZED VIEW routing_audit_summary_7d IS
  'High-level audit summary for /api/admin/auto-route/audit endpoint. '
  'Refreshed every 10 minutes by bg.MaterializedViewRefresher. '
  'Created by migration 632 (2026-08-31).';

-- NOTE: no initial REFRESH here — CREATE MATERIALIZED VIEW populates the
-- view, and the background refresher keeps it current from then on.

COMMIT;
