-- Migration 903: 给分区父表 public.sessions 补 (updated_at DESC, session_id DESC) 索引。
--
-- ═══════════════════════════════════════════════════════════════════════
-- 为什么需要它（★ 全部为 2026-10-24 生产只读实测，结论可复算）
-- ═══════════════════════════════════════════════════════════════════════
--
-- 【症状】GET /api/admin/turns/sessions 生产恒 500。
--   2833 发布后 REPEAT=5 基线：500,500,500,500,200
--   ⇒ min 30.003s / med 30.013s / max 54.510s。
--   252 的 pg_stat_statements 抓到**真实生产 SQL**：
--     calls=1 total=24904.6ms rows=21 ⇒ 单次 24.9s 才返回 20 行。
--
-- 【★ 根因：ORDER BY s.updated_at DESC 没有任何索引能提供该序】
--   admin/turns_sessions.go 的会话列表排序键是 (updated_at DESC, session_id DESC)，
--   而 public.sessions 的既有索引里：
--     idx_sessions_status (status, updated_at DESC)   ← 需要 status 等值
--     idx_sessions_tenant (tenant_id, created_at DESC) ← created_at，不是 updated_at
--     idx_sessions_tenant_session_partition_date
--     sessions_session_id_partition_date_key (session_id, partition_date)
--   ⇒ **没有一条能按 (updated_at DESC, session_id DESC) 出序**。
--   ⇒ 规划器只能 Seq Scan 全表 + top-N Sort ⇒ ★ Sort 必须把 439,106 行
--   **全部物化**才能吐出第一行 ⇒ 上层的 Limit 无法提前停 ⇒ 整条 JOIN 链跑满。
--
-- 【★ 节点级拆解（252 EXPLAIN ANALYZE，2026-10-24）】
--   变体 B（去掉 session_titles LATERAL 的单变量对照）Execution Time 23,836.829 ms：
--     · 外层 Append 产出 439,106 行（sessions 439,564 行 / 49 个 partition_date /
--       6 个已挂子分区）—— 为拿 21 行把 43.9 万行全 JOIN 了一遍
--     · 去重 SubPlan（MAX(partition_date) 跨月去重）：**439,570 次循环** ×
--       6 个分区 Index Only Scan = 6,363,231 buffer hits ≈ **10.5s**
--     · session_dim JOIN：1,754,606 hits / 439,106 次循环 ≈ **4.2s**
--     · session_summaries JOIN：1,741,045 hits / 439,106 次循环 ≈ **1.6s**
--     · top-N Sort：**0.47s** ⇒ ★ 瓶颈不是排序，是「必须先算完 43.9 万行」
--     · JIT：2.8s（估算成本 1.69M 触发）
--
-- 【★ 一条被实测否证的路线（记下来，免得再犯）】
--   「session_titles 缺 scoped_session_id 最左前缀索引 ⇒ 每次 LATERAL 全表扫」
--   —— 事实成立（session_titles 只有 UNIQUE (task_id, scoped_session_id) 与
--   (generated_at DESC)），但**量级不够**。单变量 A/B 实测：
--     A 原查询（带 st LATERAL）：Execution Time **32,089.911 ms**，shared hit 3,918,898
--     B 只去掉 st LATERAL    ：Execution Time **23,836.829 ms**，shared hit 9,876,509
--   ⇒ 该 LATERAL 只占 **8.25s（26%）** ⇒ 只补 session_titles 索引，端点仍要 ~24s。
--
-- 【★ 修复效果（252 事务内建索引 + EXPLAIN ANALYZE + ROLLBACK ⇒ 生产零持久变更）】
--   · 生产查询**原样不改**，只加索引：
--       Limit (actual time=0.881..3.639 rows=21) ⇒ **3.639 ms**
--       Buffers: shared hit=640（原 9,876,509）⇒ **~15,400×**
--   · 计划切换：Merge Append + 每个子分区一条
--       `Index Scan using sessions_2026_XX_updated_at_session_id_idx`
--       ⇒ Limit 变成「惰性拉取」，取够 21 行即停 ⇒ 整条 JOIN 链只跑 ~22 行
--   · 去重 SubPlan：439,570 次循环 → **22 次**
--   · generic plan 复核（PREPARE + 6 次 EXECUTE 后第 7 次）：仍是 Index Scan，
--     2.141 ms ⇒ ★ 参数化的 LIMIT 不会让规划器退回全表扫（不是阻塞项）
--
-- 【★ 口径零变化（本迁移不改任何查询）】
--   · 同一 REPEATABLE READ 快照内，生产查询与「改写形态」各跑一次，
--     两个结果集 **22 行（含表头）逐字一致** ⇒ 索引不改变任何一行的取值。
--   · 「改写形态」（sessions-only 子查询内含 ORDER BY + LIMIT）在同一条件下
--     实测 5.838 ms，与「只加索引不改查询」的 3.639 ms 同量级
--     ⇒ 改写**不带来额外收益** ⇒ 本迁移**只加索引**：改动面最小、口径零风险。
--
-- 【★ 覆盖范围】分区父表建索引自动传播到 6 个既有子分区
--   （sessions_2026_07/08/09/10/11 + default）与未来分区。
--
-- 【锁与时长】
--   public.sessions 是**热写表**：domains/session/v2/session_aggregator.go:216 的
--   upsertSessionSnapshot 每请求一次 `INSERT ... ON CONFLICT (session_id,
--   partition_date) DO UPDATE` ⇒ 构建期持 SHARE 锁会阻塞该分区写。
--   分区父表不支持 CREATE INDEX CONCURRENTLY ⇒ 用普通 CREATE INDEX：
--   · lock_timeout=60s：抢不到锁快速失败、整体回滚、稍后重跑
--     （882/876/891/901 同款；startup_ddl_lock_guard_gate_test 钉住
--     强锁 DDL 必须带 lock_timeout）
--   · statement_timeout=10min：索引构建本身可能很久，而 lock_timeout 只管
--     锁等待（共享 PG 服务端 statement_timeout 30s 会掐长索引构建，
--     57014 击穿后迁移永不记账，成为永久卡点）
--   · 实测构建时长：热缓存 600ms；一次冷缓存观测 ≈27s（此前一条 42s 的
--     全表扫查询把页缓存冲掉之后）⇒ ⇒ 该迁移应落在低峰窗口。
--
-- 【幂等】IF NOT EXISTS。Down：按名删父表索引（父表 DROP 级联删分区侧子索引，
-- PG 按 pg_index.indisparent 关联，不按名字 grep）。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

CREATE INDEX IF NOT EXISTS idx_sessions_updated_at_session_id
    ON public.sessions (updated_at DESC, session_id DESC);

COMMIT;

-- POST_CONDITION:
--   SELECT indexname FROM pg_indexes
--    WHERE schemaname = 'public'
--      AND indexname = 'idx_sessions_updated_at_session_id';
--   -- 分区侧子索引按 pg_index.indisparent 关联核对（877 台账口径：
--   -- 不按名字 grep —— PG 会按子表名自动改名）。
--   -- 预期收益（EXPLAIN 口径，2026-10-24 实测）：
--   --   GET /api/admin/turns/sessions 的会话列表查询
--   --   从 Seq Scan + top-N Sort（439,106 行物化）改为
--   --   Merge Append + Index Scan（惰性取 21 行）
--   --   ⇒ 32,089.911 ms → 3.639 ms，shared hit 9,876,509 → 640。
