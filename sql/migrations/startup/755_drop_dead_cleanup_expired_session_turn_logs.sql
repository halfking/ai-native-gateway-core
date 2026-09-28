-- ============================================================================
-- Migration 755: drop dead cleanup_expired_session_turn_logs()
--
-- Purpose: 收口 handoff §19 #7 —— 全仓自 2026-09-27 起已确认零生产调用方
--          （仓内 grep 0 个 Go 调用 / 0 个 pg_cron 注册 / 0 个 shell 调度）。
--          session_turn_logs 的真实清理路径在
--          cleanup_session_turn_logs_by_ttl (migration 753)，由
--          bg.PartitionManager.cleanupSessionTurnLogsByTTL 调度。
--
--   R72-D04-D06 审计（2026-09-27）保留意见：仓库无法证明生产 PG 实例上没
--   有 pg_cron / 外部 job 在调这个函数。Operator 在生产部署前应 grep
--   pg_stat_statements / pg_cron / pg_job 二次确认；本迁移是 idempotent
--   DROP，库侧无残留即通过，有残留则是「及时暴露」的清理调用方。
--
-- 编号契约：755 是 origin/main 上 sql/migrations/startup/ 当前的下一空闲号
--          （753/754 已落；755 提交前须再 git ls-tree 复核）。
-- ============================================================================

BEGIN;

DROP FUNCTION IF EXISTS cleanup_expired_session_turn_logs();

-- Ledger self-registration (710/734/738/740/742/753 惯例)。带存在性守卫：
-- 一次性测试库 (TEST_PG_URL 直灌裸 SQL) 没有 installer 基座的
-- schema_migrations 表，守卫使迁移在两种环境都可执行。
DO $$
BEGIN
    IF to_regclass('public.schema_migrations') IS NOT NULL THEN
        INSERT INTO public.schema_migrations (version, description)
        VALUES (
            '755',
            'drop dead cleanup_expired_session_turn_logs() (no production caller since 430; live path is cleanup_session_turn_logs_by_ttl in 753). Handoff §19 #7 closed.'
        )
        ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
    END IF;
END $$;

COMMIT;