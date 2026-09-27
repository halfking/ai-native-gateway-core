-- ============================================================================
-- Migration 753 down: remove session_turn_logs configurable TTL scaffolding.
--
-- Down 契约：
--   ① DROP FUNCTION cleanup_session_turn_logs_by_ttl(int) — 上游 SQL/Go
--      在迁移族内会被同步移除 (settings.spec_lifecycle.go 写 753 起不再
--      引用；partition_manager 调用点也回退到旧 cleanup_expired_*
--      风格)。这里只清 SQL 端的本体。
--   ② DROP INDEX IF EXISTS idx_session_turn_logs_expires_at — 与 430 的
--      idx_session_turn_logs_expires 同列，删之不丢语义。
--   ③ schema_migrations 簿记按 703 down 惯例移除（append-only 默认；
--      此处仅在显式 down 通道执行）。
-- ============================================================================

BEGIN;

DROP FUNCTION IF EXISTS cleanup_session_turn_logs_by_ttl(int);

DROP INDEX IF EXISTS public.idx_session_turn_logs_expires_at;

DO $$
BEGIN
    IF to_regclass('public.schema_migrations') IS NOT NULL THEN
        DELETE FROM public.schema_migrations WHERE version = '753';
    END IF;
END $$;

COMMIT;
