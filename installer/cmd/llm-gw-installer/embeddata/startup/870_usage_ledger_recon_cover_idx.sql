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
--   父表上已有一个 `usage_ledger_ts_tenant_cover_agg_idx`，它是 `ON ONLY`、
--   **0 字节**、且没有分区带它——看起来像「索引没落到分区」。
--   实测为假：分区创建路径（create_next_month_partitions / ensure_usage_ledger_partition
--   都用 `CREATE TABLE ... PARTITION OF usage_ledger`）会**各自**建等价索引并自动改名
--   （如 usage_ledger_2026_09_ts_tenant_id_prompt_tokens_completion__idx）。
--   关键差别在于 **`ON ONLY` 会抑制级联**：本文件用普通 CREATE INDEX，
--   PG 会把它建到**所有现存分区**，而 `PARTITION OF` 建出的**未来分区自动继承**
--   父表索引 ⇒ 不会像 ON ONLY 那样留下「下个月又缺」的缺口。
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
