-- Migration 485: Add raw_model_name column to request_logs
--
-- 日期: 2026-08-13
--
-- Background
-- ──────────
-- 2026-08-13 审计发现（生产 154）: integrity_fingerprint_drift 后台 worker 每次
-- 扫描报 SQLSTATE 42703：
--   column "raw_model_name" does not exist
-- 错误，导致上游指纹漂移检测失效。
--
-- Root cause:
--   bg/integrity_fingerprint_drift.go:154-189 多个 CTE 在 FROM request_logs
--   时显式 SELECT / GROUP BY raw_model_name，但 sql/objects/tables/
--   request_logs.sql canonical 定义不含此列。base schema 启动时该表
--   inline CREATE 漏了 raw_model_name。
--
--   注意: request_logs 表已有 provider_model / client_model / outbound_model /
--   canonical_model / model_chosen 等模型相关列，raw_model_name 是
--   integrity_fingerprint_drift 特定的别名。code-vs-schema drift 的历史 bug。
--
-- Fix
-- ────
-- 在 startup/485 加 raw_model_name 列，IF NOT EXISTS 幂等。本次**不**改
-- code (避免 PR 范围扩散)；下次 task 可提独立 PR 让代码统一使用
-- raw_model_name 或改用现有 provider_model 列。
--
-- Idempotent: 是（IF NOT EXISTS）
-- Down: 见 485_*.down.sql

BEGIN;

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS raw_model_name TEXT;

COMMIT;

-- POST_CONDITION: SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'raw_model_name'
