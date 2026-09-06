-- Migration: Add indexes for credential heatmap queries
-- Created: 2026-09-06
-- Purpose: Optimize time-series aggregation queries for the heatmap visualization

-- Core index: supports time range + credential + model filtering
-- This index enables efficient queries with WHERE credential_id + ts range + model
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_request_logs_heatmap_core
ON request_logs (
  credential_id,
  ts DESC,
  (LOWER(COALESCE(outbound_model, client_model)))
)
WHERE ts >= NOW() - INTERVAL '30 days'
  AND COALESCE(is_self_test, FALSE) = FALSE;

-- Covering index: includes aggregation columns to avoid table lookups
-- This index enables index-only scans for common heatmap queries
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_request_logs_heatmap_covering
ON request_logs (
  credential_id,
  (LOWER(COALESCE(outbound_model, client_model))),
  ts DESC
)
INCLUDE (success, latency_ms, error_kind, request_id)
WHERE ts >= NOW() - INTERVAL '30 days'
  AND COALESCE(is_self_test, FALSE) = FALSE;

-- Index for tenant-scoped queries (tenant_admin access pattern)
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_request_logs_tenant_heatmap
ON request_logs (
  tenant_id,
  credential_id,
  ts DESC
)
WHERE ts >= NOW() - INTERVAL '30 days'
  AND COALESCE(is_self_test, FALSE) = FALSE;

-- Analyze tables after index creation
ANALYZE request_logs;
