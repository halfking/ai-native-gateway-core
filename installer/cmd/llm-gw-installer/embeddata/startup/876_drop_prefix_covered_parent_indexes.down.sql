-- Rollback for migration 876_drop_prefix_covered_parent_indexes.sql.
-- Recreates the three narrow parent indexes. No data is touched.
--
-- ⚠ 这不是「零成本回滚」，请先读完再决定：
--
--   1. 必须建在**父表**上且**不带 ON ONLY** —— 带 ON ONLY 会留下
--      0 字节空壳、不会级联到分区（870 的文件头专门记了这个坑：
--      既有的 usage_ledger_ts_tenant_cover_agg_idx 就是那个形态）。
--      代价是：每建一条，所有分区都会被重新构建一次，
--      大分区（2026-09 = 1,416,772 行）实测秒级，但确实要时间与 IO。
--
--   2. 回滚会把每月约 243 MiB 的复发一并带回来 —— 也就是把 876 想解决的
--      问题原样装回去。如果只是想临时缓解某个查询的计划退化，
--      优先考虑在父表上重建**其中一条**而不是三条全建。
--
--   3. PG 不支持在分区父表上 CREATE/DROP INDEX CONCURRENTLY，
--      与 up 迁移同理走单事务链 + lock_timeout=2s（抢不到锁快速失败、
--      整体回滚、稍后重跑）。低峰窗口执行。
--
-- 幂等：已存在则只校验键列、形态不对就 loudly 失败，不默默重建
-- （与 870 的同名分治同形态）。
--
-- ★ 为什么这里是三段显式守卫而不是一个循环：
--   循环要跨表跨列，最省事的写法是 ARRAY[ROW(...)]::record[]，
--   但匿名 ROW 放进数组后会被擦成 record 类型，r.f1 这种字段访问取不到值
--   （实测会报 "record f1 has no field" 一类错误）。
--   三条索引各不相同，显式写反而更短也更好读 —— 别为了省三行去踩类型坑。

BEGIN;

SET LOCAL statement_timeout = '10min';
SET LOCAL lock_timeout = '2s';

-- ① usage_ledger (request_id)
DO $recreate_876_a$
BEGIN
  IF to_regclass('public.idx_usage_ledger_part_request_id') IS NULL THEN
    CREATE INDEX idx_usage_ledger_part_request_id ON public.usage_ledger (request_id);
    RAISE NOTICE '876 down: recreated idx_usage_ledger_part_request_id';
  ELSIF NOT EXISTS (
    SELECT 1 FROM pg_index
    WHERE indexrelid = to_regclass('public.idx_usage_ledger_part_request_id')
      AND indrelid = 'public.usage_ledger'::regclass
      AND indnkeyatts = 1 AND indnatts = 1 AND NOT indisunique
      AND pg_get_indexdef(indexrelid, 1, true) = 'request_id'
  ) THEN
    RAISE EXCEPTION
      '876 down: idx_usage_ledger_part_request_id exists but is not the expected single-column index on usage_ledger';
  ELSE
    RAISE NOTICE '876 down: idx_usage_ledger_part_request_id already present and matches';
  END IF;
END
$recreate_876_a$;

-- ② usage_ledger (ts)
DO $recreate_876_b$
BEGIN
  IF to_regclass('public.idx_usage_ledger_part_ts') IS NULL THEN
    CREATE INDEX idx_usage_ledger_part_ts ON public.usage_ledger (ts);
    RAISE NOTICE '876 down: recreated idx_usage_ledger_part_ts';
  ELSIF NOT EXISTS (
    SELECT 1 FROM pg_index
    WHERE indexrelid = to_regclass('public.idx_usage_ledger_part_ts')
      AND indrelid = 'public.usage_ledger'::regclass
      AND indnkeyatts = 1 AND indnatts = 1 AND NOT indisunique
      AND pg_get_indexdef(indexrelid, 1, true) = 'ts'
  ) THEN
    RAISE EXCEPTION
      '876 down: idx_usage_ledger_part_ts exists but is not the expected single-column index on usage_ledger';
  ELSE
    RAISE NOTICE '876 down: idx_usage_ledger_part_ts already present and matches';
  END IF;
END
$recreate_876_b$;

-- ③ sessions (session_id)
DO $recreate_876_c$
BEGIN
  IF to_regclass('public.idx_sessions_session_id') IS NULL THEN
    CREATE INDEX idx_sessions_session_id ON public.sessions (session_id);
    RAISE NOTICE '876 down: recreated idx_sessions_session_id';
  ELSIF NOT EXISTS (
    SELECT 1 FROM pg_index
    WHERE indexrelid = to_regclass('public.idx_sessions_session_id')
      AND indrelid = 'public.sessions'::regclass
      AND indnkeyatts = 1 AND indnatts = 1 AND NOT indisunique
      AND pg_get_indexdef(indexrelid, 1, true) = 'session_id'
  ) THEN
    RAISE EXCEPTION
      '876 down: idx_sessions_session_id exists but is not the expected single-column index on sessions';
  ELSE
    RAISE NOTICE '876 down: idx_sessions_session_id already present and matches';
  END IF;
END
$recreate_876_c$;

COMMIT;
