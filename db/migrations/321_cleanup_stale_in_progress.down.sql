-- Rollback migration 321: cleanup_stale_in_progress

-- 1. 删除 pg_cron 定时任务（如果存在）
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_cron') THEN
        PERFORM cron.unschedule('cleanup-stale-requests');
        RAISE NOTICE 'pg_cron job unscheduled: cleanup-stale-requests';
    END IF;
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'Failed to unschedule pg_cron job (may not exist): %', SQLERRM;
END $$;

-- 2. 删除清理函数
DROP FUNCTION IF EXISTS cleanup_stale_in_progress_requests();

-- 注意：不回滚已清理的数据，因为那些记录确实是超时失败的
