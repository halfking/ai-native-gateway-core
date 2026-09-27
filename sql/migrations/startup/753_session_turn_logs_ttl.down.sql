-- ============================================================================
-- Migration 753 down: remove session_turn_logs configurable TTL scaffolding.
--
-- Down 契约：
--   ① DROP FUNCTION cleanup_session_turn_logs_by_ttl(int) — Go 侧调用点在
--      bg/partition_manager.go（runCleanup step 11）随本迁移族一同回退。
--      注意：回退后 session_turn_logs 重新变成「无界增长」——因为
--      cleanup_expired_session_turn_logs()（430）虽然定义存在却从无调用方，
--      历史上从未真正清理过。回退不是回到「既有行为」，是回到「既有的
--      无清理状态」。
--   ② 不删任何索引：本迁移最终稿**未新增索引**。初稿曾建
--      idx_session_turn_logs_expires_at，与 430:275 的
--      idx_session_turn_logs_expires 同列重复，已在批判式审计中移除。
--      若某个环境已经跑过初稿，可安全执行
--      `DROP INDEX IF EXISTS public.idx_session_turn_logs_expires_at;`
--      清理残留，语义无损（430 的索引仍在）。
--   ③ schema_migrations 簿记按 703 down 惯例移除（append-only 默认；
--      此处仅在显式 down 通道执行）。
-- ============================================================================

BEGIN;

DROP FUNCTION IF EXISTS cleanup_session_turn_logs_by_ttl(int);

DO $$
BEGIN
    IF to_regclass('public.schema_migrations') IS NOT NULL THEN
        DELETE FROM public.schema_migrations WHERE version = '753';
    END IF;
END $$;

COMMIT;
