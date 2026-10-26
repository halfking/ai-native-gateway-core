-- 873_usage_ledger_ts_cover_cache_agg_idx.sql
-- 2026-10-18：把 856 的覆盖索引扩成**含缓存 token 列的超集**。
--
-- 缺陷（生产实测，154 / 共用库 172.16.2.210）：
--   `/api/admin/usage/cache-economics` 在 30 天默认窗口上稳定 500，
--   journalctl 记 `cache-economics query failed` / context deadline exceeded，
--   duration 恒卡在 handler 的 10s 预算上（usage_enhanced.go:642）。
--
-- 为什么慢（EXPLAIN (ANALYZE, BUFFERS) 实测，**同轮交错对照**，不是前后两次测）：
--   该端点要的四列是 COUNT(*) / SUM(prompt_tokens) / SUM(cost_usd) /
--   SUM(cache_read_tokens)，全是对窗口的**纯求和**（响应里没有按天分解）。
--   窗口 30 天 = usage_ledger 1,478,030 行。
--
--   变体 A（四列，含 cache_read_tokens）：
--       Parallel Index Scan using usage_ledger_2026_09_ts_idx
--         Buffers: shared hit=209,361        Execution Time: 3239 / 3426 ms
--   变体 B（同上，去掉 cache_read_tokens）：
--       Parallel Index Only Scan using usage_ledger_2026_09_ts_tenant_id_prompt_tokens_completion__idx
--         Buffers: shared hit= 19,245        Execution Time: 1502 / 1462 ms
--
--   两轮交错、计划形态 4/4 稳定 ⇒ **计划切换是确定的，耗时绝对值不是**
--   （该宿主当时 load average 33 / 4 核，被别的会话遗留的递归 grep 吃满，
--     绝对值只能当上界看；但 buffer 计数是确定量）。
--
-- 根因：856 的 INCLUDE 列表是
--     INCLUDE (prompt_tokens, completion_tokens, cost_usd, latency_ms, success)
--   **不含 cache_read_tokens**。于是一旦查询要多这一列，
--   planner 就只能放弃 856 建的 index-only scan，退回
--   `usage_ledger_2026_09_ts_idx` 的 Parallel Index Scan —— 逐行回堆，
--   1,478k 行里 1,039,470 行落在 9 月分区，多读 ~190k 个 buffer（约 1.5GB）。
--   ★ 也就是说 856 的加速对这条查询**整体失效**：它服务的是「按时间窗聚合用量」
--     这族查询，而这族里带缓存 token 的那部分拿不到任何收益。
--
-- 为什么改成「重建超集」而不是「再加一条索引」：
--   usage_ledger 是全站最热的写表，每个请求都落一行。再加一条
--   (ts, tenant_id) 前缀、与 856 高度冗余的索引，等于给写路径加一份长期
--   写放大，换一条只在特定查询里才用得上的读取路径。
--   本迁移建完超集索引后**立即按名删掉 856 的两条**，
--   任一时刻都至少有一条 (ts, tenant_id) 开头的覆盖索引可用。
--
-- 列序沿用 856 的取舍并补上缓存列：键 (ts, tenant_id)，
--   - 超管（无 tenant 过滤）：ts 范围即可定位，全部列在索引里 ⇒ index-only
--   - 租户档（AND tenant_id = $2）：tenant_id 是第二列，同样 index-only
--
-- 上线方式（大表，参照 870 的同名分治）：
--   PG **不支持**在分区父表上 CREATE INDEX CONCURRENTLY，因此走普通
--   CREATE INDEX（**不带 ON ONLY**，870 已实测：不带才会级联到分区），
--   在 psql 单事务链内执行。
--     ① lock_timeout=2s —— 抢不到锁就快速失败，不排在写流量后面；
--     ② statement_timeout=10min —— 覆盖共享 PG 服务端 30s 的默认值
--        （849/855/856 同形，否则部署链上会 57014）。
--   本库逐分区 heap（必须逐分区求和，父表 pg_total_relation_size 返回 0）：
--     usage_ledger_2026_09 = 261 MB   usage_ledger_2026_10 =  79 MB
--     usage_ledger_hot    =  16 MB   2026_08 / 2026_11 = 空月
--   合计不足 360MB heap，按 870 实测的构建速率是**十秒量级**。
--   建议低峰应用，并观察 pg_stat_progress_create_index。
--
-- ⚠️ 与 870 相同的验收陷阱：**不要按父表索引大小判成败**。
--   父表上会显示 0 bytes 且 `pg_get_indexdef` 带 `ON ONLY` —— 那是分区父表的
--   正常渲染。分区上的索引名由**列名**自动改写（名字里不含 `cache`），
--   按名字 grep 会得出「没建」的假结论（870 已踩过一次）。验收见文末。
--
-- 回滚：873_..._down.sql 删新索引并按原样重建 856 的两条，不动数据。

BEGIN;

SET LOCAL statement_timeout = '10min';
SET LOCAL lock_timeout = '2s';

-- 分区父表：级联到每个现存分区；未来分区经 PARTITION OF 自动继承。
CREATE INDEX IF NOT EXISTS usage_ledger_ts_tenant_cache_agg_idx
    ON public.usage_ledger (ts, tenant_id)
    INCLUDE (prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, cost_usd, latency_ms, success);

-- hot 表：视图是 UNION ALL，两臂形状不同会让 planner 在其中一臂退回回堆，
-- 收益减半 —— 必须同时给 hot 补同形状索引（沿用 856 的这条理由）。
CREATE INDEX IF NOT EXISTS usage_ledger_hot_ts_tenant_cache_agg_idx
    ON public.usage_ledger_hot (ts, tenant_id)
    INCLUDE (prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, cost_usd, latency_ms, success);

-- ★ 只有确认新索引**已经建出来且可用**，才敢删 856 的旧索引。
--   否则一旦新索引构建失败，这里就会把这条聚合查询唯一的覆盖索引一起带走，
--   把 cache-economics 的故障扩散到 usage summary / by-model 整族。
DO $drop_856_superseded$
DECLARE
  v_new   regclass := to_regclass('public.usage_ledger_ts_tenant_cache_agg_idx');
  v_newhot regclass := to_regclass('public.usage_ledger_hot_ts_tenant_cache_agg_idx');
BEGIN
  IF v_new IS NULL OR v_newhot IS NULL THEN
    RAISE EXCEPTION '873: 超集覆盖索引未建成，拒绝删除 856 的旧覆盖索引';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_index WHERE indexrelid = v_new   AND indisvalid AND indisready) THEN
    RAISE EXCEPTION '873: usage_ledger_ts_tenant_cache_agg_idx 无效，拒绝删除旧覆盖索引';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_index WHERE indexrelid = v_newhot AND indisvalid AND indisready) THEN
    RAISE EXCEPTION '873: usage_ledger_hot_ts_tenant_cache_agg_idx 无效，拒绝删除旧覆盖索引';
  END IF;

  DROP INDEX IF EXISTS public.usage_ledger_ts_tenant_cover_agg_idx;
  DROP INDEX IF EXISTS public.usage_ledger_hot_ts_tenant_cover_agg_idx;
END
$drop_856_superseded$;

COMMIT;

-- r1030 教训：凡「可能不存在的对象」的 COMMENT 都在 to_regclass 判定后下发。
DO $comment_873_index$
BEGIN
  IF to_regclass('public.usage_ledger_ts_tenant_cache_agg_idx') IS NOT NULL THEN
    COMMENT ON INDEX usage_ledger_ts_tenant_cache_agg_idx IS
      '873: usage_ledger「按时间窗聚合用量」覆盖索引的超集版 —— 在 856 的 INCLUDE 之外补上 cache_read_tokens / cache_write_tokens。856 那条不含缓存 token 列，于是任何带 SUM(cache_read_tokens) 的窗口聚合都会退回 Parallel Index Scan 逐行回堆：30 天 1,478k 行实测 buffers 209,361 / 3.2s，删掉该列即转 Index Only Scan buffers 19,245 / 1.5s。本索引取代 856 的 usage_ledger_ts_tenant_cover_agg_idx（同一 (ts, tenant_id) 前缀，不并存以免给最热的写表加冗余写放大）。';
  END IF;
  IF to_regclass('public.usage_ledger_hot_ts_tenant_cache_agg_idx') IS NOT NULL THEN
    COMMENT ON INDEX usage_ledger_hot_ts_tenant_cache_agg_idx IS
      '873: 本迁移的超集覆盖索引在 hot 表上的同形状副本。usage_ledger_with_current_month 是 UNION ALL，两臂形状不同会让 planner 在其中一臂退回回堆，收益减半。';
  END IF;
END
$comment_873_index$;

-- ---------------------------------------------------------------------------
-- 怎么验收（★ 沿用 870 的教训：别按父表大小、别按索引名判成败）
--
-- 分区索引由 PG 按**列名**自动改名，名字里不含 `cache`，必须按定义匹配：
--
--   SELECT i.indrelid::regclass::text AS partition,
--          pg_size_pretty(pg_relation_size(i.indexrelid)) AS idx_size,
--          i.indisvalid AND i.indisready AS valid
--   FROM pg_index i
--   WHERE i.indrelid::regclass::text LIKE 'usage_ledger%'
--     AND pg_get_indexdef(i.indexrelid) LIKE '%INCLUDE (prompt_tokens, completion_tokens, cache_read_tokens%'
--   ORDER BY 1;
--
-- 期望：4 个分区 + hot 各一行且 valid=t。真正的收益判据是执行计划转为
-- **Index Only Scan**（下面这条就是端点真跑的那条聚合）：
--
--   EXPLAIN (ANALYZE, BUFFERS)
--   SELECT COUNT(*), SUM(ul.prompt_tokens), SUM(ul.cost_usd), SUM(ul.cache_read_tokens)
--   FROM usage_ledger_with_current_month ul
--   WHERE ul.ts >= now() - interval '30 days' AND ul.ts < now();
--
-- 应用前：Parallel Index Scan，shared hit=209,361（4/4 实测）
-- 应用后：Parallel Index Only Scan，shared hit ≈ 19k 量级
