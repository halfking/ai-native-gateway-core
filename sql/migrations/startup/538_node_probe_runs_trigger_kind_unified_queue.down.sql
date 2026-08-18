-- =============================================================================
-- Migration 538 DOWN: 恢复 trigger_kind CHECK 到 425 状态
-- 2026-08-18
--
-- 注意：执行 down 前必须先清理所有 trigger_kind IN
--   ('periodic','admin','integrity_probe_planner','selfcheck','external_async')
-- 的现存行，否则 ADD CONSTRAINT 会失败。
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 536 DOWN: 恢复 trigger_kind CHECK（移除 536 增量）==='

-- 防御：536 增量行如果还在，必须先删除或重映射，否则 ADD CONSTRAINT 会失败。
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM public.node_probe_runs
        WHERE trigger_kind IN ('periodic','admin','integrity_probe_planner','selfcheck','external_async')
        LIMIT 1
    ) THEN
        RAISE EXCEPTION
            '536 DOWN aborted: node_probe_runs still contains 536-only trigger_kind values; clean them up first.';
    END IF;
END
$$;

-- Refuse the rollback before changing either constraint if 538-only queue
-- rows remain. This keeps the rollback atomic at the migration-runner level.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM public.credential_probe_queue
        WHERE source = 'selfcheck'
        LIMIT 1
    ) THEN
        RAISE EXCEPTION
            '538 DOWN aborted: credential_probe_queue still contains selfcheck rows; remap or drain them first.';
    END IF;
END
$$;

ALTER TABLE public.node_probe_runs
    DROP CONSTRAINT IF EXISTS node_probe_runs_trigger_kind_check;

-- 425 状态
ALTER TABLE public.node_probe_runs
    ADD CONSTRAINT node_probe_runs_trigger_kind_check CHECK (
        trigger_kind IN (
            'request_failure',
            'manual',
            'credential_recovery',
            'sync_request'
        )
    );

COMMENT ON CONSTRAINT node_probe_runs_trigger_kind_check ON public.node_probe_runs IS
    '425: trigger_kind 枚举 —— request_failure | manual | credential_recovery | sync_request';

ALTER TABLE public.credential_probe_queue
    DROP CONSTRAINT IF EXISTS credential_probe_queue_source_check;
ALTER TABLE public.credential_probe_queue
    ADD CONSTRAINT credential_probe_queue_source_check CHECK (
        source IN ('request_failure', 'periodic', 'external_async', 'admin',
                   'integrity_probe_planner')
    );

\echo '--- trigger_kind CHECK 已恢复 425 状态 ---'
\echo '=== 536 DOWN 完成 ==='