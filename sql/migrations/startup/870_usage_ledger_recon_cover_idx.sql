-- 870_usage_ledger_recon_cover_idx.sql
-- 2026-10-17：usage_ledger 月度对账聚合的覆盖索引（24h 审计轮慢 SQL 轮）。
--
-- 缺陷（生产实测，245 / 共用库 172.16.2.210）：
--   domains/providerprofile/pg_reconciliation_store.go:285 的
--   `MonthlyUsageByProvider` 走
--     WITH deduped AS (
--       SELECT DISTINCT ON (request_id) provider_id, prompt_tokens,
--              completion_tokens, total_tokens, cost_usd
--       FROM usage_ledger_with_current_month
--       WHERE ts >= $1 AND ts < $2 AND provider_id IS NOT NULL
--       ORDER BY request_id, ts DESC)
--   pg_stat_statements 实测 87 次 / 均值 129.2s / 最大 533.9s / 累计 187.3 分钟。
--
-- 为什么慢（EXPLAIN 实测，非推测）：
--   月窗（2026-09）计划走 `usage_ledger_2026_09_request_id_idx` 扫 1,416,772 行
--   并**逐行回表**——leading 列是 request_id 的索引**不携带**
--   provider_id / prompt_tokens / completion_tokens / total_tokens / cost_usd；
--   且 ts 范围谓词落在索引扫描**之后**（Filter）而非 Index Cond，推不下去。
--
-- 改法：(request_id, ts DESC) INCLUDE (provider_id, prompt_tokens,
--        completion_tokens, total_tokens, cost_usd)
--   键序与 DISTINCT ON 的 ORDER BY 逐列同向，INCLUDE 覆盖全部载荷 ⇒
--   Index Only Scan，零回表；ts 谓词同时下推为 Index Cond。
--
-- ★ 实测收益（生产同库，2026-10-17，事务内建索引后 ROLLBACK，零残留）：
--     基线        Index Scan      313,546 行  buffers hit=355,257  36,480.768 ms
--     加本索引    Index Only Scan 313,546 行  buffers hit= 66,964  10,082.199 ms
--   ⇒ 执行时间 −72%，缓冲访问 −81%。建索引本身 406,160 行 / 74MB heap 仅 **2.693 s**。
--   按行数与 heap 比例外推，9 月分区（1,416,772 行 / 261MB heap）约 10s 量级。
--
-- ★ 为什么建在**父表**上（这是与既有 0 字节索引的关键差别）：
--   父表上已有一个 `usage_ledger_ts_tenant_cover_agg_idx`，它同样是 0 字节、
--   而各分区另有自动改名的等价索引 —— **单看「父表 0 字节」判不出成败**。
--   见下面「怎么验收」一节：父表的 0 字节与 `ON ONLY` 字样是**分区父表的
--   正常渲染**，真正的判据是**分区上有没有**。
--   本文件用普通 CREATE INDEX（**不带 ON ONLY**）：`ON ONLY` 作为**创建选项**
--   会抑制向分区的级联，而本文件要的就是级联。
--
-- 上线方式（大表，参照 867 的同名分治）：
--   PG **不支持**在分区父表上 CREATE INDEX CONCURRENTLY，因此本文件在
--   psql 单事务链内执行、走普通 CREATE INDEX，构建期对**每个分区**持 ShareLock。
--   实测构建极短（9 月约 10s 量级），且：
--     ① lock_timeout=2s —— 抢不到锁就**快速失败**，不排在写流量后面；
--     ② 索引已存在（运维预先按分区建好）时只校验不重建。
--   建议在低峰窗口执行。
--
-- 回滚：870_..._down.sql 按名删索引，不动数据。
--
-- ---------------------------------------------------------------------------
-- 怎么验收（★ 2026-10-17 实测踩过，别再按父表大小判成败）
--
-- 应用后**父表上** `idx_usage_ledger_recon_cover` 会显示为 **0 bytes**，且
-- `pg_get_indexdef` 一定带 `ON ONLY` 字样 —— **这是分区父表的正常渲染**，
-- 不是「索引没建出来」。分区父表的索引本体只是个模板，容量计在分区上。
--
-- 验收必须查**分区级**是否存在，且**按定义匹配、不能按索引名匹配**：
-- PG 会用**列名**给分区索引自动改名（如
-- `usage_ledger_2026_09_request_id_ts_provider_id_prompt_token_idx`），
-- 名字里**不含**父索引名的 `cover` —— 按名字 grep 会得出「没建」的假结论
-- （本轮就这样误判过一次）。
--
--   SELECT i.indrelid::regclass::text AS partition,
--          ic.relname, pg_size_pretty(pg_relation_size(ic.oid)) AS idx_size,
--          i.indisvalid AND i.indisready AS valid
--   FROM pg_index i
--   JOIN pg_class ic ON ic.oid = i.indexrelid
--   JOIN pg_class tc ON tc.oid = i.indrelid
--   WHERE pg_get_indexdef(i.indexrelid) LIKE '%INCLUDE (provider_id, prompt_tokens%'
--   ORDER BY 1;
--
-- 期望：4 个分区各一行且 valid=t（应用后实测 2026-09 = 158MB、2026-10 = 45MB，
-- 两个空月分区各 8192 bytes）。真正的收益判据是执行计划转为
-- **Index Only Scan** 且 ts 谓词进入 Index Cond。

BEGIN;

SET LOCAL statement_timeout = '10min';
SET LOCAL lock_timeout = '2s';

-- Accept a same-name prebuild only when it is valid AND has the exact intended
-- shape; otherwise fail loudly rather than silently accepting a wrong index.
DO $guard_870_index$
DECLARE
  v_idx regclass := to_regclass('public.idx_usage_ledger_recon_cover');
BEGIN
  IF v_idx IS NOT NULL THEN
    IF NOT EXISTS (
      SELECT 1 FROM pg_index
      WHERE indexrelid = v_idx AND indisvalid AND indisready
    ) THEN
      RAISE EXCEPTION '870: idx_usage_ledger_recon_cover is invalid; repair the build before retrying';
    END IF;
    IF NOT EXISTS (
      SELECT 1 FROM pg_index i
      JOIN pg_class c ON c.oid = i.indexrelid
      JOIN pg_am am ON am.oid = c.relam
      WHERE i.indexrelid = v_idx
        AND i.indrelid = 'public.usage_ledger'::regclass
        AND am.amname = 'btree'
        AND NOT i.indisunique AND i.indexprs IS NULL
        -- 键列恰为 (request_id, ts)，且 ts 为 DESC
        AND i.indnatts = 7 AND i.indnkeyatts = 2
        AND pg_get_indexdef(i.indexrelid, 1, true) = 'request_id'
        AND pg_get_indexdef(i.indexrelid, 2, true) = 'ts'
        AND i.indoption[1] = 3
        -- INCLUDE 列恰为那 5 个载荷列
        AND pg_get_indexdef(i.indexrelid, 3, true) = 'provider_id'
        AND pg_get_indexdef(i.indexrelid, 4, true) = 'prompt_tokens'
        AND pg_get_indexdef(i.indexrelid, 5, true) = 'completion_tokens'
        AND pg_get_indexdef(i.indexrelid, 6, true) = 'total_tokens'
        AND pg_get_indexdef(i.indexrelid, 7, true) = 'cost_usd'
    ) THEN
      RAISE EXCEPTION '870: idx_usage_ledger_recon_cover definition mismatch; repair the index before retrying';
    END IF;
  END IF;
END
$guard_870_index$;

DO $create_870_index$
BEGIN
  IF to_regclass('public.idx_usage_ledger_recon_cover') IS NULL THEN
    -- 建在父表上：PG 会级联到所有现存分区；未来分区经 PARTITION OF 自动继承。
    CREATE INDEX idx_usage_ledger_recon_cover
        ON public.usage_ledger (request_id, ts DESC)
        INCLUDE (provider_id, prompt_tokens, completion_tokens, total_tokens, cost_usd);
  END IF;
END
$create_870_index$;

COMMIT;

-- r1030 教训：凡「可能不存在的对象」的 COMMENT 都在 to_regclass 判定后下发。
DO $comment_870_index$
BEGIN
  IF to_regclass('public.idx_usage_ledger_recon_cover') IS NOT NULL THEN
    COMMENT ON INDEX idx_usage_ledger_recon_cover IS
      '870: usage_ledger 月度对账聚合的覆盖索引 —— 承重读方是 providerprofile 月度用量对账（request_id 去重 + 按 provider 汇总）。基线走 request_id 单列索引逐行回表，实测 36.5s/313k 行、缓冲 355k；加本索引转 Index Only Scan，10.1s、缓冲 67k（−72% / −81%）。ts 谓词同时下推为 Index Cond。建在父表上以级联到现存与未来分区——不要改成 ON ONLY，那会像既有的 usage_ledger_ts_tenant_cover_agg_idx 一样留下 0 字节的空壳。';
  END IF;
END
$comment_870_index$;
