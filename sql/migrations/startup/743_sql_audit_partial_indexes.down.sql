-- 743 down: 移除 252 SQL 日志审计第六轮的部分索引。
-- 父壳 DROP 会连带已 ATTACH 的分区子索引；分区独立名子索引与 hot/outbox
-- 索引逐一显式 DROP。回滚后行为退回 743 前：outbox done 清理回到 645MB
-- 全表扫（45min ×255 次 med 4.2s）、digest 回填回到全分区空扫（30s+
-- 击杀循环）。除非确认索引引发问题，不建议回滚。

DROP INDEX CONCURRENTLY IF EXISTS public.idx_session_aggregate_outbox_done_completed_at;
DROP INDEX IF EXISTS public.idx_session_turns_digest_null;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_session_turns_hot_digest_null;

-- 分区子索引若未被父壳 ATTACH（中断残留），按名清理。
DO $$
DECLARE
  part text;
BEGIN
  FOR part IN
    SELECT c.relname
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    WHERE i.inhparent = 'public.session_turns'::regclass
  LOOP
    IF to_regclass(format('public.%I', part || '_digest_null_idx')) IS NOT NULL THEN
      EXECUTE format('DROP INDEX IF EXISTS public.%I', part || '_digest_null_idx');
    END IF;
  END LOOP;
END $$;
