-- Migration: 999b_system_probe_run_tokens
-- Purpose: 给 system_probe_runs 补 total_tokens 列 —— 让探针运行的 token 计量真的能落库
-- Phase: 第 31 轮（存量缺口收口轮 · 2026-11-26）
-- Author: huangxt
-- Date: 2026-11-26
--
-- ★ 背景：★ 「生产 SQL 引用的列必须有投递来源创建」门（internal/schemadelivery，
--   2026-11-26 第 30 轮新立）第一次校准就抓到了这条：bg/systemmonitor/audit.go 的
--   INSERT 直接写 system_probe_runs.total_tokens ⇒ 而创建这一列的文件
--   （sql/migrations/domain/346_system_probe_run_tokens.sql）**不在任何投递通道里**
--   ⇒ 新装库与部署库上这条 INSERT 恒 42703 ⇒ 探针审计行静默丢失（写侧 best-effort：
--   monitor.go 的 writeAudit 不检查返回值）。
--
-- ★ 为什么原先没有这一列（★ 这不是漏建，是当时的口径）：
--   admin/systemmonitor_handlers.go 的读侧注释记着 2026-10-20 的真库实测结论：
--   「system_probe_runs **没有** token 列（它只记状态与时延）」⇒ 读侧当时把
--   `COALESCE(SUM(total_tokens), 0)` 那个聚合删掉了，响应键 total_tokens_1h 被
--   **故意保留、值恒为 0**，并注明「要统计探针消耗的 token 需要另接计量表」。
--   ⇒ 346 那份件躺在 sql/migrations/domain/ 里（不被任何通道消费）⇒ 写侧要、
--   读侧不要，两边就这么错开了半年。
--
-- ★ 本迁移的取舍（owner 2026-11-26 决定）：
--   ① 投递这一列（把 346 的内容搬进真通道）—— 写侧不再 42703、token 值真的落库；
--   ② **同时恢复读侧的聚合**（admin/systemmonitor_handlers.go 同批改）—— 否则列建了
--      也没人读，页面的 Token 计数仍然是 0，功能等于没修。
--   ⇒ 两处必须同批落地：只投递不改读侧 = 数据落了但看不到；只改读侧不投递 = 42703。
--
-- ★ 语义边界（照抄读侧 2026-10-20 那条注释，仍然成立）：
--   这一列记的是**探针运行**的 token 量（来自 result.Result.TotalTokens），
--   不是会话/请求口径的 token（那是 usage_ledger／session_turns 的事）。
--   两套口径不要混。
--
-- ★ 幂等：ADD COLUMN IF NOT EXISTS ⇒ 重放无害。
-- ★ 锁守卫（installer 门 startup_ddl_lock_guard_gate_test.go：版本 > 870 且含强锁 DDL
--   ⇒ 必须自带 lock_timeout）：本件含 ALTER TABLE（拿 ACCESS EXCLUSIVE 锁）⇒ 声明
--   5s lock_timeout / 10min statement_timeout（同 999a 口径）⇒ 等锁超时就失败退出，
--   不在等待期间堵住全库。

\set ON_ERROR_STOP on

SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

ALTER TABLE public.system_probe_runs
    ADD COLUMN IF NOT EXISTS total_tokens BIGINT NOT NULL DEFAULT 0;

COMMENT ON COLUMN public.system_probe_runs.total_tokens IS
    '探针运行消耗的 token 量（口径＝探针运行，非请求口径）；2026-11-26 随 999b 投递';

COMMIT;
