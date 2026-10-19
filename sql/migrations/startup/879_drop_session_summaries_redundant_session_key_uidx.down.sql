-- Rollback for migration 879_drop_session_summaries_redundant_session_key_uidx.sql.
-- Recreates the redundant unique index on session_key. No data is touched.
--
-- ⚠ 这不是「零成本回滚」，请先读完再决定：
--
--   1. 重建的是一条**与主键键集逐字重复**的唯一索引 ⇒ 磁盘回到 +67.2 MiB，
--      而且每次写要多维护一份 B-tree（写放大回到双倍）——
--      也就是把 879 想解决的问题原样装回去。
--      如果只是想临时缓解某个查询的计划退化，优先考虑别的办法。
--
--   2. 建索引要扫全表并持 SHARE 锁 ⇒ 低峰窗口执行；建期间该表的
--      ON CONFLICT (session_key) 写入会被拖慢（仲裁器多了一个候选）。
--
--   3. 若存量数据里出现重复 session_key ⇒ 建索引失败（整条回滚）。
--
-- 幂等：已存在则只校验键列，形态不对就 loudly 失败，不默默重建。

BEGIN;

SET LOCAL statement_timeout = '10min';
SET LOCAL lock_timeout = '2s';

DO $recreate_uidx_879$
BEGIN
    IF to_regclass('public.session_summaries_session_key_uidx') IS NULL THEN
        CREATE UNIQUE INDEX session_summaries_session_key_uidx
            ON public.session_summaries (session_key);
        RAISE NOTICE '879 down: recreated session_summaries_session_key_uidx';
    ELSIF NOT EXISTS (
        SELECT 1
          FROM pg_index
         WHERE indexrelid = to_regclass('public.session_summaries_session_key_uidx')
           AND indnkeyatts = 1
           AND indkey::text = (
                SELECT a.attnum::text
                  FROM pg_attribute a
                 WHERE a.attrelid = 'public.session_summaries'::regclass
                   AND a.attname = 'session_key'
           )
    ) THEN
        RAISE EXCEPTION '879 down: session_summaries_session_key_uidx exists with unexpected key columns';
    END IF;
END $recreate_uidx_879$;

COMMIT;
