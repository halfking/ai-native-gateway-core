-- =============================================================================
-- Migration 425: node_probe_runs.trigger_kind 扩展允许 'sync_request'
-- Created: 2026-07-17
-- Author:  gateway maintainers (同步探测特性)
--
-- 背景 (2026-07-17 同步探测特性):
--   新增 bg/node_probe.go:ProbeSync —— 当 executor 走到 no_candidate 分支
--   时，请求路径上同步 hold 一段时间并行探测同模型候选。探测结束后写一行
--   node_probe_runs 审计行，trigger_kind 需要区分：
--     - 'request_failure' — 后台 worker 走的失败触发起飞
--     - 'manual'          — 运维手动触发
--     - 'credential_recovery' — 凭据恢复触发的探测
--     - 'sync_request'    — (新增) 同步探测，由 inbound 请求发起
--
--   原 CHECK 约束只允许前三种；425 扩展为允许 'sync_request'，幂等可重跑。
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 425: node_probe_runs.trigger_kind CHECK 扩展 sync_request ==='

ALTER TABLE public.node_probe_runs
    DROP CONSTRAINT IF EXISTS node_probe_runs_trigger_kind_check;

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
    '425: trigger_kind 枚举扩展 —— 新增 sync_request（同步探测，由 inbound 请求 no_candidate 路径发起）';

\echo '--- trigger_kind CHECK 已扩展 ---'
\echo '=== 425 完成 ==='