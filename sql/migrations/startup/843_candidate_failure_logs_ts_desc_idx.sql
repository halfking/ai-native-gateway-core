-- 843_candidate_failure_logs_ts_desc_idx.sql
-- 2026-10-07：给 candidate_failure_logs 两臂补 (ts DESC)，让无过滤的 max(ts) 从
--              「全量扫索引」变成「读一条索引条目」。
--
-- 背景（runbook §10.106.28）：§10.106.26 结尾标注「同族检查（未做）」——
--   只查了 credential_model_index 一族，不宣称其余族同构。本迁移是那次逐族核对
--   的第一份**阳性**结论；同批核对的 request_logs 族为**阴性**（见下）。
--
-- 缺陷形态：与 842 完全同构，只是列不同。
--   842 是「GROUP BY (credential_id, raw_model)，但索引首列是 bucket」；
--   本条是「MAX(ts) 无过滤，但**没有任何**索引以 ts 打头」。
--   PostgreSQL 只在**首列**上做 max/min 优化；ts 是第二列时，
--   只能把整个索引条目流式扫一遍再取最大。
--
-- 证据一：两臂索引首列普查（只读，154 实测）
--   candidate_failure_logs        6 个索引，其中以 ts 打头的 **0 个**
--   candidate_failure_logs_hot    6 个索引，其中以 ts 打头的 **0 个**
--   最接近的是 (credential_id, ts) / (provider_id, ts) —— ts 都在第二列。
--
-- 证据二：实际计划（EXPLAIN，不带 ANALYZE，未执行）
--   SELECT max(ts) FROM candidate_failure_logs;
--     Finalize Aggregate (cost=4566.45)
--       -> Parallel Append
--          -> Parallel Index Only Scan using candidate_failure_logs_2026_09_credential_id_ts_idx
--                (cost=0.42..1957.86 rows=47375)     <-- 扫 4.7 万条索引条目
--          -> Parallel Index Only Scan using candidate_failure_logs_2026_10_credential_id_ts_idx1
--                (cost=0.29..1145.83 rows=12724)     <-- 再扫 1.3 万条
--          -> Parallel Seq Scan on candidate_failure_logs_2026_11 (rows=118)
--   对照组 hot 臂：Index Only Scan using ..._hot_provider_id_ts_idx (cost=171.79)
--   ⇒ 父表臂是 hot 臂的 **26 倍**，代价全部花在「把索引从头读到尾」。
--
-- 证据三：这条语句被调用的规模（pg_stat_statements，本库，已剔除 EXPLAIN）
--   告警语句（两次 max(ts) 探测）58,001 次 / 均 501.2 ms / 合计 **8.07 小时**。
--   其中 `max(ts) FROM candidate_failure_logs_with_current_month` **无 WHERE 子句**
--   ⇒ 每次都吃满上面那个全索引扫描。
--
-- 本地正向验证（一次性 PG 17.11 集群，复刻生产形状：ts 为第二列 + 无 ts 首列索引）
--   加索引前：Append / Seq Scan，actual rows=55000，Execution 2.736 ms，cost 1261.51
--   加索引后：Limit / Index Only Scan，**actual rows=1**，Execution **0.036 ms**，cost 0.74
--   ⇒ 76 倍，且读入行数从 55,000 降到 1。这不是外推，是同形状实测。
--
-- ★ 本迁移只加索引，**不改任何查询、不改语义、零行为变化**。
--
-- ⚠️ 上线代价（机制已在 runbook §10.106.27 实测，勿照抄旧说法）：
--   `CREATE INDEX CONCURRENTLY` 在分区父表上**直接被拒**
--   （ERROR: cannot create index on partitioned table concurrently），
--   故本文件用普通 CREATE INDEX。
--   普通 CREATE INDEX 对**父表与全部分区**取 **ShareLock**（不是 ACCESS EXCLUSIVE）：
--   与 RowExclusiveLock 冲突 ⇒ **写被阻塞**；与 AccessShareLock 不冲突 ⇒ **读照常**。
--   ⇒ 窗口内表现为「写入报错、查询照常」，**只需挡写、不需要停服**。
--   规模：candidate_failure_logs 3 分区合计约 190 MB（154 实测），
--   窗口在秒级；仍建议维护窗口应用并观察 `pg_stat_progress_create_index`。
--
-- ⚠️ 收益不预告：索引能把 max(ts) 从「扫 6 万条索引条目」变成「读 1 条」，
--   但**带 ts 过滤的窗口查询**能不能转成 Index Only Scan，取决于可见性映射，
--   取决于该分区最近是否被 VACUUM 过。上线后必须重测 pg_stat_statements 的
--   total_exec_time，降幅有限则不要继续在索引上加码。
--
-- 同批核对的阴性结论（一并留档，避免下轮重复排查）：
--   · **request_logs 族不是 842 同构缺陷**。它的热点语句
--     `WITH win AS (SELECT credential_id, COUNT(*) ... FROM request_logs_with_current_month
--       WHERE ts >= now() - interval $3 AND credential_id IS NOT NULL GROUP BY credential_id)`
--     **带时间窗**，扫描面被 ts 约束，不存在 842 那种「每次全量 35.6 万行」。
--     且父表臂实测走 `Index Only Scan`（用 (tenant_id, ts DESC) 索引的第二列
--     做 Index Cond，cost 2400），**没有退化**。
--   · request_logs 父表确实没有无条件 (ts DESC) 索引（唯一的
--     idx_request_logs_discard_events_ts 是 partial：WHERE discard_events IS NOT NULL），
--     但它有 38 个索引且实测计划已可接受 ⇒ **不动**。不为对称而对称。
--
-- 回滚：.down.sql 删两个索引，不动任何数据。

BEGIN;

-- 分区父表：在父级建索引会级联到每个分区（pg_indexes 里显示 ON ONLY 的即为此形态）。
CREATE INDEX IF NOT EXISTS candidate_failure_logs_ts_desc_idx
    ON public.candidate_failure_logs (ts DESC);

-- hot 表同形状，保证两臂一致 —— 视图是 UNION ALL，两臂形状不同会让
-- planner 在其中一臂上退回全表/全索引扫描（正是本缺陷的成因）。
CREATE INDEX IF NOT EXISTS candidate_failure_logs_hot_ts_desc_idx
    ON public.candidate_failure_logs_hot (ts DESC);

COMMIT;
