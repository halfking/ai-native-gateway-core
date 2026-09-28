-- Migration 755 (down): re-create cleanup_expired_session_turn_logs()
--
-- Pure rollback to the 430-era shape. Same body as 430:336 and 513:42 —
-- preserved verbatim to keep semantics identical to pre-755 production.
--
-- 警告：本次回滚不会自动恢复 Go 侧 domains/session/v2.CleanupExpiredLogs()
--      （已在 chore/turn-logs-deadcode 的前一个 commit 删除，且本迁移系列
--       无意恢复）。如果未来要重新启用活清理路径，请走 753 的
--       cleanup_session_turn_logs_by_ttl，不要再注册这个 24h 硬编码版。

BEGIN;

CREATE OR REPLACE FUNCTION cleanup_expired_session_turn_logs()
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    deleted_count INT;
BEGIN
    DELETE FROM public.session_turn_logs
    WHERE expires_at < NOW();

    GET DIAGNOSTICS deleted_count = ROW_COUNT;

    IF deleted_count > 0 THEN
        RAISE NOTICE 'Cleaned up % expired session turn logs', deleted_count;
    END IF;
END;
$$;

COMMENT ON FUNCTION cleanup_expired_session_turn_logs() IS
    'Cleanup expired session turn logs (older than 24 hours).
     Should be called by bg worker or cron job every hour.
     Created: 2026-07-17, Migration 430; restored by migration 755.down.';

COMMIT;