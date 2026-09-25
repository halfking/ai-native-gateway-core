-- Migration 746: session_mirror_outbox source CHECK 扩展 'claim'
--
-- 2026-09-25 252 PG SQL 日志审计第八轮（D12 拍板）：final-success claim
-- UPDATE（telemetry/client.go claimSessionFinalSuccessExec）置位成功后，
-- 在同一事务内登记一行 session_mirror_outbox（source='claim'）作为
-- 补偿兜底。动机（storage-merge 观察期第 4/5 例 G2 缺失，09-22）：
--   - v1 侧 is_final_success=TRUE 与 v2 侧 session_turns 行的写入是两阶段
--     （同事务 claim → commit → hooks → best-effort 镜像）。hooks 阶段
--     失败且 outbox 登记也失败（或进程崩溃窗口）时，claimed 行永久无
--     turns 且无任何持久痕迹——重放器天然无效（无登记可重放），
--     GLOBAL_G2 对账只能发现、不能修复。
--   - 修复：claim 置位成功（RowsAffected>0）的行无条件登记 outbox，
--     reaper 重放走 v2.Write（request_id 幂等）——镜像已成功时重放为
--     幂等 no-op 后删行；镜像缺失时重放即为补写。claimed 行频率实测
--     252 为 9 行/24h（请求 7.4 万/24h），登记与重放开销可忽略。
--
-- 本迁移只扩 source CHECK 枚举；登记逻辑在
-- domains/hooks/observability/telemetry/client.go（registerFinalSuccessClaimOutbox）。

BEGIN;

ALTER TABLE public.session_mirror_outbox
    DROP CONSTRAINT IF EXISTS session_mirror_outbox_source_check;

ALTER TABLE public.session_mirror_outbox
    ADD CONSTRAINT session_mirror_outbox_source_check
    CHECK (source IN ('hook', 'backfill', 'claim'));

COMMIT;
