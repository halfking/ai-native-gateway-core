-- 842_credential_model_index_latest_bucket_idx.sql
-- 2026-10-07：给「每个 (credential_id, raw_model) 的最新 bucket」这条聚合补上索引。
--
-- 背景（runbook §10.106.26）：
--   autoroute 的索引刷新 SQL（`autoroute/index.go` 的 refreshIndexSQL，
--   `admin/auto_route.go` 同款）以
--     WITH latest_bucket AS (
--         SELECT credential_id, raw_model, MAX(bucket)
--         FROM credential_model_index_with_current_month
--         GROUP BY credential_id, raw_model)
--   开头，是 pg_stat_statements 里**全库第 2 名**的语句：
--     total_exec_time = 135,396,957 ms（37.6 h）/ 188,357 次 / 均 719 ms。
--   该视图是 credential_model_index_hot（2,361 行）UNION ALL
--   credential_model_index（354,153 行 / 3 分区）= **356,514 行**，
--   而去重后的 (credential_id, raw_model) 只有 **1,043 组**（342 倍差距）。
--
-- 根因：现有唯一索引的**首列是 bucket**
--   CREATE UNIQUE INDEX credential_model_index_bucket_cred_model_key
--     ON ONLY public.credential_model_index (bucket, credential_id, raw_model)
-- 首列不对 ⇒ 既服务不了 GROUP BY credential_id, raw_model，
-- 也服务不了「按对定位再 ORDER BY bucket DESC LIMIT 1」。
-- hot 表同样是 (bucket, credential_id, raw_model)，外加单列 (credential_id)，
-- **没有 (credential_id, raw_model, bucket) 这个形状的索引**。
-- 于是 PostgreSQL 只能对 35.6 万行做 HashAggregate，去产出 1,043 行。
--
-- 实测（154 同机，EXPLAIN ANALYZE）：
--   MAX(bucket) GROUP BY          Planning 13.5 ms / Execution 189.4 ms
--   SELECT DISTINCT 同一对列       Planning 16.9 ms / Execution 258.8 ms
-- ⇒ 「换成 DISTINCT 就快了」是错的（258.8 > 189.4）。
--
-- ★ 本迁移只加索引，**不改任何查询、不改语义、零行为变化**。
--
-- ⚠️ 为什么不加 CONCURRENTLY：
--   PostgreSQL 不支持在分区父表上 CREATE INDEX CONCURRENTLY（本写法在本库 PG 17
--   亦不可用）。普通 CREATE INDEX 会对每个分区取 ACCESS EXCLUSIVE 直到建完。
--   354,153 行分摊到 3 个分区，实测单分区规模下窗口应在秒级，
--   **仍建议在维护窗口应用**，并观察 `pg_stat_progress_create_index`。
--
-- ⚠️ 收益不预告：索引只能把「扫堆行」换成「扫窄索引条目」，
--   **不能减少 35.6 万条要读的行数**（那是 PostgreSQL 的 loose index scan
--   能力问题，PG 18 之前无解）。
--   本次部署后必须重测 pg_stat_statements 的 total_exec_time；
--   若降幅有限，下一步是**改查询**（按 hot 优先 + LATERAL），
--   那会改变语义（只在 hot 出现过的 pair 才算「最新」），
--   **属于产品决策，不在本迁移内**。
--
-- 回滚：.down.sql 删两个索引，不动任何数据。

BEGIN;

-- 分区父表：在父级建索引会级联到每个分区（pg_indexes 里显示为 ON ONLY 的
-- 就是这种「分区索引」形态，它本身不存数据，只作模板）。
CREATE INDEX IF NOT EXISTS credential_model_index_cred_model_bucket_idx
    ON public.credential_model_index (credential_id, raw_model, bucket DESC);

-- hot 表：补同形状索引，保证两臂一致 —— 视图是 UNION ALL，两臂形状不同会让
-- planner 在其中一臂上退回全表扫描。
CREATE INDEX IF NOT EXISTS credential_model_index_hot_cred_model_bucket_idx
    ON public.credential_model_index_hot (credential_id, raw_model, bucket DESC);

COMMIT;