-- 856_usage_ledger_ts_cover_agg_idx.sql
-- 2026-10-13：给「按时间窗聚合用量指标」这条查询补覆盖索引。
--
-- 缺陷（真机 iPhone 6s / iOS 15.8.8 上发现，非模拟器推测）：
--   移动端「用量」页整页报错，屏幕上是
--     加载失败 / summary query failed
--   生产日志（245 canary@8782，journalctl）：
--     "op":"summary query failed","caller":"usage.go:154",
--     "err":"timeout: context deadline exceeded"
--     同一批里紧跟着 /api/usage/by-model 同款 500（usage.go:610）。
--   ⇒ `/api/usage/summary` 与 `/api/usage/by-model` 在**任何**窗口都 500，
--     不是数据问题，是查询跑不完被 handler 的 5s 预算掐掉。
--
-- 预算在哪：
--   admin/usage.go:89 `context.WithTimeout(r.Context(), 5*time.Second)`
--   （by-model 同款，:557 一带）
--
-- 查询长什么样（usage.go:115-128）：
--   SELECT COUNT(*), SUM(prompt_tokens), SUM(completion_tokens),
--          SUM(cost_usd), AVG(latency_ms),
--          SUM(CASE WHEN success THEN 1 ELSE 0 END)/COUNT(*)
--   FROM usage_ledger_with_current_month
--   WHERE ts >= now() - ($1 * INTERVAL '1 day')   [+ AND tenant_id = $2]
--
-- 为什么跑不完（EXPLAIN ANALYZE 实测，245 线上库，days=7）：
--   Execution Time: 2279.749 ms   ← 空载已经 2.3s
--   Planning Time:  22.523 ms
--   计划：
--     Parallel Index Scan using usage_ledger_2026_10_ts_idx
--       (actual time=7.275..2098.751 rows=66431 loops=3)
--   66,431 / 69,527 行来自 usage_ledger_2026_10 单个分区（142 MB）。
--   今天是 10-13 ⇒ 「近 7 天」几乎覆盖 10 月全月 ⇒ 这个范围天然是大头。
--
-- 关键点：现有索引只服务 **过滤**，不服务 **取列**。
--   idx_usage_ledger_part_ts / usage_ledger_hot_ts_idx 都只有 (ts)。
--   于是 6.6 万行每一行都要回堆取 5 个指标列 —— 时间全花在随机堆访问上。
--   ★ 这就是为什么「已经有 ts 索引」却仍然慢：索引范围对了，取列还是回堆。
--
-- 修法：覆盖索引（INCLUDE），把过滤列与聚合列放进同一条窄索引，
--   让这条聚合走 **index-only scan**，不再回堆。
--   ★ 列顺序刻意是 (ts, tenant_id) INCLUDE (...)：
--     - 超管（无 tenant 过滤）：ts 范围即可定位，全部列都在索引里 ⇒ index-only
--     - 租户档（AND tenant_id = $2）：tenant_id 作为第二列也在索引里，
--       不需要回堆就能过滤 ⇒ 同样 index-only
--   若把 tenant_id 放进 INCLUDE，索引会宽一截而收益相同。
--
-- 与 842 同款取舍：不加 CONCURRENTLY。
--   理由沿用 842 的实测结论（CREATE INDEX CONCURRENTLY 在分区父表上被拒），
--   这里建在分区父表上，由 PostgreSQL 级联到每个分区。
--   ⚠️ 窗口内 **写被阻塞**（ShareLock 与 RowExclusiveLock 冲突），
--     读不受影响。本库逐分区体量（必须逐分区求和，父表 pg_total_relation_size
--     返回 0）：
--       usage_ledger_2026_09 = 720 MB
--       usage_ledger_2026_10 = 142 MB
--       usage_ledger_2026_08 / 2026_11 = 40 kB
--       usage_ledger_hot       = 15 MB
--     合计不到 900 MB，按 842 实测的 ~203k 行/s 量级，窗口是**秒级**。
--     仍建议低峰应用，并观察 pg_stat_progress_create_index。
--
-- ⚠️ 收益不预告：覆盖索引减少的是**回堆**，不减少要读的行数。
--   应用后必须重测 usage summary 的 Execution Time；
--   若仍 >1s，下一步是给这两条查询单独放宽 5s 预算（改 handler 超时，
--   属于行为变更，需单独评审），**不在本迁移内**。
--
-- 回滚：.down.sql 删两个索引，不动任何数据。

BEGIN;

-- 分区父表：级联到每个分区。
CREATE INDEX IF NOT EXISTS usage_ledger_ts_tenant_cover_agg_idx
    ON public.usage_ledger (ts, tenant_id)
    INCLUDE (prompt_tokens, completion_tokens, cost_usd, latency_ms, success);

-- hot 表：视图是 UNION ALL，两臂形状不同会让 planner 在其中一臂退回回堆，
-- 收益减半 —— 必须同时给 hot 补同形状索引。
CREATE INDEX IF NOT EXISTS usage_ledger_hot_ts_tenant_cover_agg_idx
    ON public.usage_ledger_hot (ts, tenant_id)
    INCLUDE (prompt_tokens, completion_tokens, cost_usd, latency_ms, success);

COMMIT;