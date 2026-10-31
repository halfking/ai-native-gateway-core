-- ===========================================================================
-- File:          sql/migrations/startup/916_session_turns_api_key_ts_index.down.sql
-- Migration:     916 (down)
-- Database:      llm_gateway
-- Purpose:       回滚 916 —— 移除会话族 api_key_id 归一表达式的 (CASE, ts DESC) 索引
--                 （父壳连带分区子索引一并消失）+ 热侧独立索引。
--
-- ── 回滚后的行为 ────────────────────────────────────────────────────────
-- 「按 api_key 过滤会话」退回 916 前：ts 索引 + 逐行 CASE 过滤 ⇒
--   252 实测（7 天窗、api_key=113）：窗口内 75,449 行全读、丢掉 37,825 行，
--   一次读点 220,401 块（根节点口径）。
--   ⇒ ★★ **除非确认索引本身引发问题（写入放大 / 空间），不建议回滚。★★
--
-- ── 锁守卫（★ 会话级 SET，不是 SET LOCAL）───────────────────────────────
-- ★ 与 up 文件同款理由（914 文件头实测）：本通道以 `psql -f` 逐文件执行，
--   **不在事务块内** ⇒ `SET LOCAL` 只打 WARNING 后完全失效 ⇒ ⇒ 用会话级 SET。
-- ★ 本文件**不**包 BEGIN/COMMIT：installer 的 applySQL 默认给每个文件加
--   --single-transaction ⇒ 显式 BEGIN 会变成嵌套事务 ⇒ 直接报错。
-- ===========================================================================

SET lock_timeout = '2s';
SET statement_timeout = '10min';

DROP INDEX IF EXISTS public.idx_session_turns_api_key_ts;
DROP INDEX IF EXISTS public.idx_session_turns_hot_api_key_ts;
