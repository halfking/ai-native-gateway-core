-- ===========================================================================
-- File:          sql/migrations/startup/920_node_probe_runs_cred_completed_idx.down.sql
-- Migration:     920 (down)
-- Database:      llm_gateway
-- Purpose:       回滚 918 —— 移除 node_probe_runs 的 (credential_id, completed_at)
--                覆盖索引。
--
-- ── 回滚后的行为 ────────────────────────────────────────────────────────
-- INV-4 双模型探测成功门退回 918 前：每次评估扫该凭据全史 run
-- （顶层凭据 ≈ 3.3~3.8 万索引项 + 堆取数，252 实测均值 4.29s/次）。
-- ⇒ ★★ 除非确认索引本身引发问题（写入放大 / 空间），不建议回滚。★★
--
-- ── 锁守卫（★ 会话级 SET；本文件不包 BEGIN/COMMIT —— installer 的 applySQL
--    默认给每个文件加 --single-transaction ⇒ 显式 BEGIN 会变成嵌套事务）────
-- ★ 用普通 DROP INDEX 而不是 DROP INDEX CONCURRENTLY：CONCURRENTLY 不能进
--   事务块，而 down 通道默认 --single-transaction（916 down 同款取舍）；
--   lock_timeout 2s 保证拿不到锁就快失败，而不是排在探针写入后面。
-- ===========================================================================

SET lock_timeout = '2s';
SET statement_timeout = '30min';

DROP INDEX IF EXISTS public.idx_node_probe_runs_cred_completed;
