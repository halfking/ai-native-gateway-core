-- dbinit:no-transaction —— 含 CREATE INDEX CONCURRENTLY（同 up 体）。
--
-- 902 down: 恢复 845 形态 —— 重建 (event_id, occurred_at) 复合索引，
--           并把 pkey 换回 (event_id) 单列体。仅在明确指令下执行
--           （回滚后 stats_event_dedup 恢复 ~265 MB 双索引形态）。

SET lock_timeout = '60s';
SET statement_timeout = '10min';

-- ① 重建 845 复合索引（先建，避免与换体步骤争锁形态）。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_stats_event_dedup_event_id_occurred_at
    ON public.stats_event_dedup (event_id, occurred_at);

-- ② 换回单列 pkey：仅当「pkey 当前带 INCLUDE」时执行。
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS stats_event_dedup_pkey_plain_tmp
    ON public.stats_event_dedup (event_id);

DO $unswap$
DECLARE
    v_pkey_name text;
BEGIN
    PERFORM set_config('lock_timeout', '60s', true);

    IF NOT EXISTS (
        SELECT 1
          FROM pg_index ix
          JOIN pg_class t     ON t.oid = ix.indrelid
          JOIN pg_namespace n ON n.oid = t.relnamespace
         WHERE n.nspname = 'public'
           AND t.relname = 'stats_event_dedup'
           AND ix.indisprimary
           AND ix.indnkeyatts = 1
           AND ix.indnatts  = 2
    ) THEN
        RAISE NOTICE '902 down: pkey is not the INCLUDE form — unswap skipped (idempotent)';
        RETURN;
    END IF;

    SELECT c.conname INTO v_pkey_name
      FROM pg_constraint c
     WHERE c.conrelid = 'public.stats_event_dedup'::regclass
       AND c.contype  = 'p';
    IF v_pkey_name IS NULL THEN
        RAISE EXCEPTION '902 down: stats_event_dedup has no primary key constraint — unexpected state';
    END IF;

    EXECUTE format('ALTER TABLE public.stats_event_dedup DROP CONSTRAINT %I', v_pkey_name);
    ALTER TABLE public.stats_event_dedup
        ADD CONSTRAINT stats_event_dedup_pkey PRIMARY KEY
        USING INDEX stats_event_dedup_pkey_plain_tmp;
END
$unswap$;

-- ③ 兜底清理（任何路径不留孤儿）。
DROP INDEX CONCURRENTLY IF EXISTS stats_event_dedup_pkey_plain_tmp;

COMMENT ON INDEX stats_event_dedup_pkey IS
    '536: stats_event_dedup 主键（902 down 恢复后的原始形态）';
