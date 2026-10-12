-- 858_request_logs_final_success_gsid_idx.sql
-- 2026-10-13：给「会话终态晋级」这条写路径补 is_final_success 的**存在性索引**。
--
-- 缺陷（生产实测，stats_reset 2026-10-10 16:11，62 小时窗口）：
--   语句：`UPDATE request_logs_hot SET is_final_success = ... WHERE request_id = $2 ...`
--          （Go 侧 `claimSessionFinalSuccess`，domains/hooks/observability/telemetry/，
--            在请求终态事务的 SAVEPOINT 内执行）
--   calls 9,439 / mean 987.3 ms / **shared_blks_hit 1,077,543,433（每次 114,159 页）**
--   = llm_gateway 全库数据库时间的 **4.06%**（9,306 分钟），排第五。
--
-- 它为什么贵：WHERE 里有两个反查，逐个探测 4 个月度分区
--   （request_logs_2026_08 / _09 / _10 / _11）的 is_final_success 集合，
--   用来保证「一个 gw_session_id 在全局只有一行 is_final_success」。
--
-- ★ 缺口本体：**hot 侧补过这个索引，月度分区侧漏了**，而且是**形状对的那条也用不上**。
--   request_logs_hot   有 uq_request_logs_hot_final_success_session
--                      = (gw_session_id) WHERE is_final_success AND gw_session_id IS NOT NULL
--                        AND gw_session_id <> ''      ← 同形状查询实测走它（cost 2.50）
--   request_logs_2026_10  带 is_final_success 前导的索引数 = **0**
--   request_logs（父表）     同上 = **0**
--   ⇒ 只剩两条都不对路的：
--     · (gw_session_id, ts DESC) WHERE gw_session_id IS NOT NULL AND gw_session_id <> ''
--       —— **部分谓词含 `gw_session_id <> ''`（字面量），而生产查询写的是
--          `gw_session_id <> $12`（参数）。PG 无法证明「参数不等式」蕴含
--          「<> '' 这个字面量不等式」⇒ 该索引对本查询不可用**
--          （实测 62 小时只有 316 次扫描，而另一条同表查询跑了 9,439 次）
--     · (tenant_id, gw_session_id, ts) WHERE gw_session_id IS NOT NULL
--       —— tenant_id 打头，本查询不约束 tenant_id ⇒ 退化成整条索引扫描 + **逐条回堆**
--       （实测 idx_scan 22,419 / idx_tup_read 1,142,281,845 / idx_tup_fetch 1,142,246,363）
--   ⇒ 每次调用在这条分��上摸约 5.1 万个索引条目、且每个都回堆 —— 与实测的
--     114,159 页/次 同一量级。
--
-- 改法：在**分区父表**上建 (gw_session_id) WHERE is_final_success AND gw_session_id IS NOT NULL，
--   由 PostgreSQL 级联到 5 个分区。查询的 `is_final_success` 与 `gw_session_id IS NOT NULL`
--   都是**可证明**蕴含 ⇒ 走 index-only scan，只读约 28,853 个窄条目（2026_10）
--   且**不回堆**，取代现在的 30.5 万条回堆。
--
-- ⚠ 两个设计决定，都不是随手写的：
--   1) **部分谓词刻意不含 `gw_session_id <> ''`**。
--      hot 那条索引含它，而本迁移不含 —— 因为本查询的参数形态无法证明它。
--      多存几个 '' 行换索引可用，这笔交换在实测里是压倒性的。
--   2) **建在父表、不加 CONCURRENTLY**。沿用 842 的实测结论：
--      CREATE INDEX CONCURRENTLY 在分区父表上被拒；普通 CREATE INDEX 会级联。
--      窗口内**写被阻塞**（ShareLock 与 RowExclusiveLock 冲突），读不受影响。
--      逐分区体量（父表 pg_total_relation_size 返回 0，必须逐分区求和）：
--        request_logs_2026_10 = 343 MB（305,112 行，其中 28,853 行 is_final_success）
--        request_logs_2026_09 = 5.6 MB（480 行）
--        其余分区近空
--      索引只存 9.5% 的行 ⇒ 体量约为表堆的 1/10 量级，窗口是秒级。
--      **仍建议低峰应用，并观察 pg_stat_progress_create_index。**
--
-- ⚠ 非唯一：唯一性由 hot 侧的 uq_request_logs_hot_final_success_session 继续承担。
--   月度分区是历史数据，只被查询做存在性判断，不需要也不该在这里重建唯一约束
--   （那会要求跨分区全局唯一，属于产品语义变更，不在本迁移内）。
--
-- 回滚：.down.sql 删该索引，不动任何数据；回滚后该语句退回 987 ms 量级。

BEGIN;

-- 分区父表：级联到全部 5 个分区（分区索引本身不存数据，只作模板）。
CREATE INDEX IF NOT EXISTS request_logs_final_success_gsid_idx
    ON public.request_logs (gw_session_id)
    WHERE is_final_success AND gw_session_id IS NOT NULL;

COMMIT;
