-- Migration 488: Add model column to request_logs_hot
--
-- 日期: 2026-08-13
--
-- Background
-- ──────────
-- 2026-08-13 审计发现（生产 154）: model_tier worker 每次扫描报
--   SQLSTATE 42703：
--   column "model" does not exist
-- 错误，导致"常用模型"动态 top-N 统计失效。
--
-- Root cause:
--   bg/model_tier.go:133-139 从 request_logs_hot 执行
--   SELECT model FROM request_logs_hot WHERE success ... GROUP BY model
--   ORDER BY count(*) DESC LIMIT N，但 sql/objects/tables/
--   request_logs_hot.sql canonical 定义不含 model 列。
--   base schema 启动时该表 inline CREATE 漏了 model。
--
--   request_logs_hot 已有 provider_model / client_model / outbound_model /
--   canonical_model / model_chosen 等 5 个模型相关列，model 是另一个
--   code-vs-schema drift 的历史 bug。
--
-- Fix
-- ────
-- 在 startup/488 加 model 列，IF NOT EXISTS 幂等。本次**不**改 code
-- (避免 PR 范围扩散)；下次 task 可提独立 PR 让代码统一使用 model
-- 或改用现有 provider_model / canonical_model 列之一。
--
-- Idempotent: 是（IF NOT EXISTS）
-- Down: 见 488_*.down.sql

BEGIN;

ALTER TABLE request_logs_hot
    ADD COLUMN IF NOT EXISTS model TEXT;

COMMIT;

-- POST_CONDITION: SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs_hot' AND column_name = 'model'
