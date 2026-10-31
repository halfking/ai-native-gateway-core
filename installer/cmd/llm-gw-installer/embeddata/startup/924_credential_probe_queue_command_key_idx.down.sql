-- ===========================================================================
-- File:          sql/migrations/startup/924_credential_probe_queue_command_key_idx.down.sql
-- Migration:     924 (down)
-- Database:      llm_gateway
-- Purpose:       回滚 924 —— 移除 credential_probe_queue 的
--                (probe_command, tenant_id, credential_id, raw_model, created_at)
--                复合索引。
--
-- ── 回滚后的行为 ────────────────────────────────────────────────────────
-- integrity_probe_planner 的 WITH pick 反连接退回 924 前形态：
-- Merge Anti Join 全量过队（252 实测单次 15,455 buffers / 生产均值 10.38s、
-- 132 次/24h 恒定在榜）。⇒ ★★ 除非确认索引本身引发问题（写入放大 /
-- 空间），不建议回滚。★★
--
-- ── 锁守卫（★ 会话级 SET；本文件不包 BEGIN/COMMIT —— installer 的 applySQL
--    默认给每个文件加 --single-transaction ⇒ 显式 BEGIN 会变成嵌套事务）────
-- ★ 用普通 DROP INDEX 而不是 DROP INDEX CONCURRENTLY：CONCURRENTLY 不能进
--   事务块，而 down 通道默认 --single-transaction（916/920 down 同款取舍）；
--   lock_timeout 2s 保证拿不到锁就快失败，而不是排在队列 DML 后面。
-- ===========================================================================

SET lock_timeout = '2s';
SET statement_timeout = '30min';

DROP INDEX IF EXISTS public.idx_credential_probe_queue_command_key_created;
