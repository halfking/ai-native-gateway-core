-- Migration 906 (down): 删掉 906 加的两条部分索引。
--
-- ═══════════════════════════════════════════════════════════════════════
-- 为什么这样删
-- ═══════════════════════════════════════════════════════════════════════
--
-- · idx_session_turns_api_key_numeric 是**分区父表**上的索引 ⇒ 按名 DROP 父表索引
--   会级联删掉各子分区侧子索引（PG 按 pg_index.indisparent 关联，**不按名字 grep**
--   —— 877 台账口径：分区侧子索引会被 PG 按子表名自动改名）。
-- · idx_session_turns_hot_api_key_numeric 是 hot 表上的普通索引 ⇒ 按名删即可。
--
-- ★ 回滚代价：删这两条索引 ⇒ enrich 查询退回「回表过滤」形态
--   ⇒ 探针批次 1,350~1,569 ms（实测）⇒ 端点 ~1.5 s。
--   ⚠ 但**不会回到 500@30s** —— 那是 R93 修的「视图 ⋈ 全量 source」形态，
--     与本迁移无关 ⇒ ★ **删索引与回滚二进制是两件事**，
--     回滚本迁移不需要回滚 R93 的代码。
--
-- 【幂等】IF EXISTS。重复执行不报错。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

DROP INDEX IF EXISTS public.idx_session_turns_api_key_numeric;
DROP INDEX IF EXISTS public.idx_session_turns_hot_api_key_numeric;

COMMIT;

-- POST_CONDITION:
--   SELECT count(*) FROM pg_indexes
--    WHERE schemaname = 'public'
--      AND indexname IN ('idx_session_turns_api_key_numeric',
--                         'idx_session_turns_hot_api_key_numeric');
--   -- 预期：0
