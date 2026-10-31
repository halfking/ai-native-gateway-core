-- dbinit:no-transaction —— 本文件含 CREATE INDEX CONCURRENTLY（PG 不允许在事务块内执行），installer 的 applySQL 据此走非事务通道（见 installer/internal/dbinit/runner.go noTransactionMarker，887/888/729/916 同款）。无该标记时全新安装会失败于 "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"。
-- ===========================================================================
-- File:          sql/migrations/startup/920_node_probe_runs_cred_completed_idx.sql
-- Migration:     920
-- Database:      llm_gateway
-- Purpose:       给 node_probe_runs 补 (credential_id, completed_at) 覆盖索引，
--                承接 INV-4「双模型探测成功门」的 24h 关联子查询。
--
-- ── 缺口本体（2026-10-29 R176 轮，252 生产只读实测，非推断）────────────────
-- 252 SQL 日志 26.4h 窗（10-28 03:37 → 10-29 06:03）：INV-4 门的形状
-- （`EXISTS (SELECT 1 FROM (SELECT DISTINCT ON (npr.raw_model_name) …
--   WHERE npr.credential_id = cmb.credential_id
--     AND npr.completed_at > now() - interval '24 hours' …) latest_run …)`，
--  唯一实现 bg/probe_policy.go credentialTwoProbeSuccessGateSQL）：
--   · 慢查 325 次 / 1,395.1s（均值 4.29s / max 9.9s）
--   · 被取消（canceling statement due to user request，即调度 ctx 到点弃 query）
--     **224 次**
--   ⇒ 合计 549 次/天，是错误面第三大族、慢查面第七大族。
--
-- 为什么慢：门需要「该凭据 24h 内的 run」，而 252 上 node_probe_runs 的
-- credential 前导索引只有一条：
--     idx_node_probe_runs_cred_model ON (credential_id, raw_model_name, started_at DESC)
-- `completed_at` **不在任何索引里** ⇒ 每次评估都要把该凭据的**全史** run
-- 逐条取回再过滤：
--   · 顶层凭据全史 ≈ 3.3~3.8 万条（cred 8/18/19 实测），24h 内只有 ~1,300 条
--   ⇒ 每次门评估 ≈ 3.5 万索引项 + 同量堆取数，只为留下 4% 的行。
--   （该门是**关联子查询**，在 probe_necessity / model_probe / daily_probe_audit /
--     today_success_probe 五个调度读点逐行求值 ⇒ 成本按候选行数放大。）
--
-- ── 形态选择 ─────────────────────────────────────────────────────────────
--   btree (credential_id, completed_at)
--     INCLUDE (raw_model_name, started_at, success)
--
--   · 前导 credential_id：门的关联键（五个读点全部传 credential 表达式）
--   · 第二键 completed_at：门的时间谓词（> now()-24h）直接成为 Index Cond，
--     扫描集从「全史 3.8 万」缩到「24h ≈ 1,300」
--   · INCLUDE 三列 = DISTINCT ON 子查询的全部输出列（raw_model_name / started_at /
--     success）⇒ **Index Only Scan**，零堆取数
--   · node_probe_runs 是普通表（非分区）⇒ 单条 CONCURRENTLY 即可；
--     TTL DELETE（partition_manager.go:1348，按 started_at）与探针写入都在跑，
--     普通 CREATE INDEX 的 SHARE 锁会阻塞它们 ⇒ 必须 CONCURRENTLY。
--
-- ── 顺带承重（同一形状的第二个读点）──────────────────────────────────────
--   bg/probe_necessity.go:449 的「上一轮探测结果」读点
--   （credential_id = $1 AND raw_model_name = $2 AND completed_at IS NOT NULL
--     ORDER BY started_at DESC LIMIT 1）在本索引上可先按 credential 前导收窄
--   （其主承索引仍是 idx_node_probe_runs_cred_model，本索引不作其承重点）。
-- ===========================================================================

-- ── 锁守卫（★ 会话级 SET，不是 SET LOCAL：本通道 psql -f 逐文件执行、
--    不在事务块内，SET LOCAL 只打 WARNING 后完全失效 —— 914/916 文件头实测）
SET lock_timeout = '2s';
SET statement_timeout = '30min';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_node_probe_runs_cred_completed
    ON public.node_probe_runs (credential_id, completed_at)
    INCLUDE (raw_model_name, started_at, success);
