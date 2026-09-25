-- 749 down：移除 usage_facts occurred_at 前导索引。
--
-- 父索引 DROP 后 attached 子索引级联消失；循环只兜尚未 ATTACH 的
-- 孤儿子索引（中断残留，744 down 同款）。注意：删除本索引会退回
-- rollup/对账每日全表扫的线性退化路径（见 up 文件头注），重建请
-- 重放 749 up 或等 gateway 首启 ensure。

DROP INDEX IF EXISTS public.idx_usage_facts_occurred_at;

DO $$
DECLARE
  part text;
BEGIN
  FOR part IN
    SELECT c.relname
    FROM pg_class c
    WHERE c.oid IN (
      SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.usage_facts'::regclass
    )
    ORDER BY c.relname
  LOOP
    EXECUTE format('DROP INDEX IF EXISTS public.%I', part || '_occurred_at_idx');
  END LOOP;
END $$;
