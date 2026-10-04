-- ===========================================================================
-- File:          sql/migrations/startup/828_supplier_errors_unified_tracked.down.sql
-- Migration:     828 (down)
-- Database:      llm_gateway
-- Purpose:        恢复 V371 形态的 supplier_errors_unified（21 列，含 source）。
--
-- ⚠ **回滚它等于把故障装回去。** 恢复 `source` 列之后，
--   `SELECT source FROM supplier_errors_unified` 在本机会重新抛
--   `cache lookup failed for attribute source of relation <oid>`
--   （只要 supplier_errors 的月分区还是 Citus columnar —— 那是 813 之外的现状）。
--   保留这个 down 是为了在**列存形态尚未回滚**（决策表 D30-a 尚未执行）时有一条退路，
--   **不是**因为「恢复旧形态是好事」。
--
-- 幂等：DROP VIEW IF EXISTS + CREATE，可安全重放。
-- 不加 CASCADE：若存在依赖视图，让它失败并回滚，而不是悄悄级联。
-- ===========================================================================
BEGIN;

DROP VIEW IF EXISTS public.supplier_errors_unified;

CREATE VIEW public.supplier_errors_unified AS
SELECT
    'hot'::text AS source,
    id, occurred_at, request_id, trace_id, tenant_id, session_id,
    provider_id, supplier, credential_id, model, attempt_seq,
    error_type, error_code, http_status, error_message,
    is_retryable, stage, latency_ms, affected_users, request_metadata
FROM public.supplier_errors_hot
UNION ALL
SELECT
    'historical'::text AS source,
    id, occurred_at, request_id, trace_id, tenant_id, session_id,
    provider_id, supplier, credential_id, model, attempt_seq,
    error_type, error_code, http_status, error_message,
    is_retryable, stage, latency_ms, affected_users, request_metadata
FROM public.supplier_errors;

ALTER VIEW public.supplier_errors_unified SET (security_invoker = true);

COMMIT;
