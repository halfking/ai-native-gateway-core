-- Migration 632: Create materialized views for routing analytics performance optimization
--
-- Purpose: Pre-aggregate hot query data for /api/admin/auto-route/analytics/* endpoints
--          to resolve timeout issues with request_logs_with_current_month_without_customer_id.
--
-- Root cause:
--   Analytics endpoints (flow, matrix, audit) query 314K+ rows with aggregations,
--   causing 15s timeouts. No index can solve GROUP BY performance on this scale.
--
-- Solution:
--   Create materialized views for 7d and 30d windows with pre-aggregated metrics.
--   Refresh every 10 minutes via background worker.
--
-- Status: active
-- Idempotent: YES
-- Rollback: down/632_routing_analytics_materialized_view.down.sql
-- Related: admin/analytics.go, admin/auto_route.go (handleAudit)
-- Changelog:
--   2026-08-31  v1.0  Initial materialized view creation
--
-- Performance target:
--   Query latency: 15s → <500ms
--   Refresh cost: ~5-10s per refresh (acceptable for 10min interval)

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
  
  -- Provider dimension (for L2→L3 flow)
  provider_id,
  
  -- Request classification
  is_auto_request,
  
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
  provider_id,
  is_auto_request,
  tenant_id;

-- Create unique index for CONCURRENTLY refresh support
CREATE UNIQUE INDEX IF NOT EXISTS routing_analytics_7d_pkey
  ON routing_analytics_7d (
    time_bucket,
    effective_task_type,
    effective_model,
    effective_work_type,
    COALESCE(provider_id, -1),
    COALESCE(is_auto_request, FALSE),
    COALESCE(tenant_id, -1)
  );

-- Create covering indexes for common query patterns
CREATE INDEX IF NOT EXISTS routing_analytics_7d_task_model_idx
  ON routing_analytics_7d (effective_task_type, effective_model);

CREATE INDEX IF NOT EXISTS routing_analytics_7d_time_idx
  ON routing_analytics_7d (time_bucket DESC);

CREATE INDEX IF NOT EXISTS routing_analytics_7d_tenant_idx
  ON routing_analytics_7d (tenant_id)
  WHERE tenant_id IS NOT NULL;

COMMENT ON MATERIALIZED VIEW routing_analytics_7d IS
  'Pre-aggregated 7-day routing analytics for /api/admin/auto-route/analytics/* endpoints. '
  'Refreshed every 10 minutes by background worker. '
  'Created by migration 632 (2026-08-31).';

-- =============================================================================
-- 2. Extended materialized view: 30-day window (optional, for future use)
-- =============================================================================
-- Uncomment when 30d window queries are added to the UI.
--
-- CREATE MATERIALIZED VIEW IF NOT EXISTS routing_analytics_30d AS
-- SELECT ... FROM request_logs_with_current_month_without_customer_id
-- WHERE ts >= NOW() - INTERVAL '30 days' ...
-- GROUP BY ...;

-- =============================================================================
-- 3. Audit summary view: simplified aggregates for /audit endpoint
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
  'Refreshed every 10 minutes by background worker. '
  'Created by migration 632 (2026-08-31).';

-- =============================================================================
-- 4. Initial population (may take 10-20s on large datasets)
-- =============================================================================

REFRESH MATERIALIZED VIEW routing_analytics_7d;
REFRESH MATERIALIZED VIEW routing_audit_summary_7d;

-- =============================================================================
-- 5. Verification
-- =============================================================================

DO $$
DECLARE
  analytics_count bigint;
  audit_count bigint;
BEGIN
  -- Check analytics view populated
  SELECT COUNT(*) INTO analytics_count FROM routing_analytics_7d;
  IF analytics_count = 0 THEN
    RAISE WARNING '632: routing_analytics_7d is empty (normal if no recent requests)';
  ELSE
    RAISE NOTICE '632: routing_analytics_7d populated with % rows', analytics_count;
  END IF;
  
  -- Check audit view populated
  SELECT COUNT(*) INTO audit_count FROM routing_audit_summary_7d;
  RAISE NOTICE '632: routing_audit_summary_7d populated with % rows', audit_count;
  
  -- Verify index created
  IF NOT EXISTS (
    SELECT 1 FROM pg_indexes
    WHERE indexname = 'routing_analytics_7d_pkey'
  ) THEN
    RAISE EXCEPTION '632: routing_analytics_7d_pkey index missing';
  END IF;
  
  RAISE NOTICE 'Migration 632 completed successfully';
END $$;

COMMIT;
