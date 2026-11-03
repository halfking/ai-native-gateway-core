-- 723 down (R40 RLS Phase 1, 2026-09-18): revert to dormant-policies state.
-- R42 (2026-09-18): to_regclass guards — candidate_failure_logs_columnar_old
-- is absent on canonical-chain databases without V359 deploy lineage; see
-- the up-migration header for the failure class.

DO $$
BEGIN
    IF to_regclass('public.attachments') IS NOT NULL THEN
            -- ★ 2026-11-04 12h 审计轮：PostgreSQL **没有** `NO ROW LEVEL SECURITY` 这个子句
    --   （合法形态只有 ENABLE / DISABLE ROW LEVEL SECURITY）⇒ 原写法是**语法错误**
    --   ⇒ 整个 BEGIN/COMMIT 事务回滚 ⇒ 该 down 迁移**从未真正执行过**。
ALTER TABLE public.attachments DISABLE ROW LEVEL SECURITY;
    ELSE
        RAISE NOTICE '723 down: table public.attachments not present, skipping';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('public.candidate_failure_logs_columnar_old') IS NOT NULL THEN
            -- ★ 2026-11-04 12h 审计轮：PostgreSQL **没有** `NO ROW LEVEL SECURITY` 这个子句
    --   （合法形态只有 ENABLE / DISABLE ROW LEVEL SECURITY）⇒ 原写法是**语法错误**
    --   ⇒ 整个 BEGIN/COMMIT 事务回滚 ⇒ 该 down 迁移**从未真正执行过**。
ALTER TABLE public.candidate_failure_logs_columnar_old DISABLE ROW LEVEL SECURITY;
    ELSE
        RAISE NOTICE '723 down: table public.candidate_failure_logs_columnar_old not present, skipping';
    END IF;
END $$;
