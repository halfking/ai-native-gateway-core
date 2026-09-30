-- Migration 484: Add status_code column to request_logs_hot
--
-- 日期: 2026-08-13
--
-- Background
-- ──────────
-- 2026-08-13 审计发现（生产 154）: credential_selfcheck_worker 每 5 分钟报
--   column rl.status_code does not exist (SQLSTATE 42703)
-- 错误，导致"24 小时内有失败的 credential"无法被 self-check worker 选出。
--
-- Root cause:
--   bg/credential_selfcheck.go:373 在 LATERAL subquery 中显式引用
--   `COALESCE(rl.status_code, 0) >= 400`，其中 rl 是 request_logs_hot 的别名。
--   但 sql/objects/tables/request_logs_hot.sql canonical 定义只有
--   upstream_status_code 列，没有 status_code。base schema 启动时
--   该表 inline CREATE 漏了 status_code 列。
--
--   代码 commit 4b5740b9c (2026-07-26) 引入此代码时假设列名为 status_code，
--   但实际上 base schema 用的是 upstream_status_code。这是一个 code-vs-schema
--   drift 的历史 bug。
--
-- Fix
-- ────
-- 在 startup/484 加 status_code 列，IF NOT EXISTS 幂等。注释对齐
-- bg/credential_selfcheck.go 代码引用的 status_code 列。
--
-- 注意: 本迁移**不**重命名 upstream_status_code，也不删它。新列与
-- upstream_status_code 数据冗余（两者都应填同一个值），但保持
-- canonical schema 与 code 引用一致。后续可提独立 task 让代码统一
-- 使用 status_code 并删除 upstream_status_code。
--
-- Idempotent: 是（IF NOT EXISTS）
-- Down: 见 484_*.down.sql

BEGIN;

ALTER TABLE request_logs_hot
    ADD COLUMN IF NOT EXISTS status_code INTEGER;

COMMIT;

-- POST_CONDITION: SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs_hot' AND column_name = 'status_code'
