-- =============================================================================
-- Migration 425 DOWN: 移除 sync_request 触发器
-- 2026-07-17
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 425 DOWN: 恢复 trigger_kind CHECK 排除 sync_request ==='

ALTER TABLE public.node_probe_runs
    DROP CONSTRAINT IF EXISTS node_probe_runs_trigger_kind_check;

-- 迁移 341 的原始版本（不包含 sync_request）。
ALTER TABLE public.node_probe_runs
    ADD CONSTRAINT node_probe_runs_trigger_kind_check CHECK (
        trigger_kind IN ('request_failure','manual','credential_recovery')
    );

\echo '--- trigger_kind CHECK 已恢复原状 ---'
\echo '=== 425 DOWN 完成 ==='