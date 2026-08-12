-- Migration 486: Add probe_revert_at column to credential_model_bindings
--
-- 日期: 2026-08-13
--
-- Background
-- ──────────
-- 2026-08-13 审计发现（生产 154）: probe_rollback revert 扫描每次报
--   SQLSTATE 42703：
--   column cmb.probe_revert_at does not exist
-- 错误，导致 tentative restore 的探测恢复 worker 无法运行。
--
-- Root cause:
--   bg/probe_rollback.go:109-110 UPDATE credential_model_bindings SET
--   probe_revert_at = NULL WHERE cmb.probe_revert_at IS NOT NULL，但
--   sql/objects/tables/credential_model_bindings.sql canonical 定义
--   不含 probe_revert_at 列。base schema 启动时该表 inline CREATE 漏了。
--
--   bg/node_probe.go:1891 同样引用 probe_revert_at。这是 2026-08 上线
--   的新 probe 系统的字段 (commit 系列 feat(probe))，base schema 升级
--   没同步包含此列。
--
-- Fix
-- ────
-- 在 startup/486 加 probe_revert_at 列 (TIMESTAMPTZ)，IF NOT EXISTS 幂等。
--
-- Idempotent: 是（IF NOT EXISTS）
-- Down: 见 486_*.down.sql

BEGIN;

ALTER TABLE credential_model_bindings
    ADD COLUMN IF NOT EXISTS probe_revert_at TIMESTAMPTZ;

COMMIT;

-- POST_CONDITION: SELECT 1 FROM information_schema.columns WHERE table_name = 'credential_model_bindings' AND column_name = 'probe_revert_at'
