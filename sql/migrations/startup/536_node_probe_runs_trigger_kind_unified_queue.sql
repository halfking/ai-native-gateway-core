-- =============================================================================
-- Migration 536: node_probe_runs.trigger_kind 扩展接受统一队列来源
-- Created: 2026-08-18
-- Author:  gateway maintainers (Agent B, 2026-08-18 全局路由审计)
--
-- 背景 (2026-08-18 global routing audit, handoff §7 P0):
--   迁移到统一 credential_probe_queue 之后，新来源 (periodic / admin /
--   integrity_probe_planner / selfcheck) 携带 task.Source 进入 ProbeService.Run，
--   但 node_probe_runs.trigger_kind 的 CHECK 只允许 request_failure /
--   manual / credential_recovery / sync_request。结果：
--     1. pgx INSERT 在 probe_service.go:373-398 直接被 _, _ = ... 吞掉，
--        审计行写失败没有任何告警。
--     2. node_probe_runs 停滞（生产 154 最新一行停在 2026-08-17 13:45），
--        探针路径看不到触发来源分布。
--   本迁移扩展枚举覆盖所有 task.Source 实际值；幂等可重跑。
--
-- 增量（不删除旧值，保持向后兼容）:
--   periodic              — 30s 周期 due state pump (node_probe.go:709)
--   admin                 — 管理 API / 取消事件 (probe_queue.go:341)
--   integrity_probe_planner — 后台整合探针规划器 (integrity_probe_planner.go:233)
--   selfcheck             — 凭证级自检 SSE (credential_selfcheck.go:187)
--   external_async        — 异步外部触发（CHECK 中已声明，预留）
--
-- 保留值:
--   request_failure, manual, credential_recovery, sync_request
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 536: node_probe_runs.trigger_kind CHECK 扩展（统一队列来源）==='

ALTER TABLE public.node_probe_runs
    DROP CONSTRAINT IF EXISTS node_probe_runs_trigger_kind_check;

ALTER TABLE public.node_probe_runs
    ADD CONSTRAINT node_probe_runs_trigger_kind_check CHECK (
        trigger_kind IN (
            -- 425 之前 + 425 增量（向后兼容）
            'request_failure',
            'manual',
            'credential_recovery',
            'sync_request',
            -- 536 增量（统一 credential_probe_queue 实际来源）
            'periodic',
            'admin',
            'integrity_probe_planner',
            'selfcheck',
            'external_async'
        )
    );

COMMENT ON CONSTRAINT node_probe_runs_trigger_kind_check ON public.node_probe_runs IS
    '536: trigger_kind 枚举扩展 —— 425 = request_failure | manual | credential_recovery | sync_request；536 = periodic | admin | integrity_probe_planner | selfcheck | external_async。完整支持统一 credential_probe_queue 的 task.Source 集合，避免 INSERT 校验拒绝后被静默吞掉。';

\echo '--- trigger_kind CHECK 已扩展 ---'
\echo '=== 536 完成 ==='