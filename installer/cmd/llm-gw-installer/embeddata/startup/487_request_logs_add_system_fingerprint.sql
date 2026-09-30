-- Migration 487: Add system_fingerprint column to request_logs
--
-- 日期: 2026-08-13
--
-- Background
-- ──────────
-- 2026-08-13 审计发现（生产 154）: 修了 485 (raw_model_name) 后下一轮
-- integrity_fingerprint_drift 扫描又报 SQLSTATE 42703：
--   column "system_fingerprint" does not exist
--
-- Root cause:
--   bg/integrity_fingerprint_drift.go:154-189 多个 CTE 在 FROM request_logs
--   时显式 SELECT / GROUP BY / WHERE IS NOT NULL system_fingerprint，但
--   sql/objects/tables/request_logs.sql canonical 定义**不含此列**。
--   base schema 启动时该表 inline CREATE 漏了 system_fingerprint。
--
--   485 修了 raw_model_name 后这个新错误才暴露（因为修复 1 列后下一个
--   缺失列就被下一个 SELECT 命中）。这是分层 code-vs-schema drift 的
--   典型表现。
--
-- Fix
-- ────
-- 在 startup/487 加 system_fingerprint 列，IF NOT EXISTS 幂等。
-- 注释对齐 cmd/gateway/main.go + bg/integrity_fingerprint_drift.go
-- 引用语义（per-(cred, model) system_fingerprint drift detector）。
--
-- Idempotent: 是（IF NOT EXISTS）
-- Down: 见 487_*.down.sql

BEGIN;

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS system_fingerprint TEXT;

COMMIT;

-- POST_CONDITION: SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'system_fingerprint'
