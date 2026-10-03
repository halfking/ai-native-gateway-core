-- ===========================================================================
-- File:          sql/migrations/startup/821_session_turns_abandoned_marker.down.sql
-- Migration:     821 (down)
-- Database:      llm_gateway
--
-- 回滚顺序与 up 相反：先删索引（它们是部分索引，删列会级联但显式更清楚），
-- 再删列。
--
-- ⚠ **本 down 会丢弃已采集的遗弃事实**：`is_abandoned = TRUE` 的行在回滚后
--    不可恢复（该信息只存在于这一列）。生产回滚前先导出：
--        SELECT tenant_id, session_id, request_id, ts
--          FROM session_turns_hot WHERE is_abandoned IS TRUE
--    稳态规模是每天个位数（§9.66 实测遗弃率 0.047%），导出成本可忽略。
-- ===========================================================================
BEGIN;

DROP INDEX IF EXISTS public.idx_session_turns_abandoned;
DROP INDEX IF EXISTS public.idx_session_turns_abandoned_parent;

ALTER TABLE public.session_turns_hot DROP COLUMN IF EXISTS is_abandoned;
ALTER TABLE public.session_turns     DROP COLUMN IF EXISTS is_abandoned;

-- 守卫与 up 对称：ADD/DROP COLUMN IF EXISTS 在「对象已不存在」时静默成功，
-- 所以要显式确认两张表都不再有这一列。
DO $$
DECLARE
  tbl  text;
  still text;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['public.session_turns', 'public.session_turns_hot'] LOOP
    SELECT column_name INTO still
      FROM information_schema.columns
     WHERE table_schema = split_part(tbl, '.', 1)
       AND table_name   = split_part(tbl, '.', 2)
       AND column_name  = 'is_abandoned';

    IF still IS NOT NULL THEN
      RAISE EXCEPTION 'migration 821 down: % 上 is_abandoned 仍然存在', tbl;
    END IF;
  END LOOP;

  RAISE NOTICE 'migration 821 down: is_abandoned removed from both session_turns faces';
END $$;

COMMIT;
