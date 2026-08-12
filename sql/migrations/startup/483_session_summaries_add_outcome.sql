-- Migration 483: Add outcome column to session_summaries
--
-- 日期: 2026-08-13
--
-- Background
-- ──────────
-- 2026-08-13 审计发现（生产 154）: session health compute 后台 worker 每 60 分钟报
--   column "outcome" of relation "session_summaries" does not exist (SQLSTATE 42703)
-- 错误，导致 session_summaries.health_score / health_grade / outcome 的写入失败。
--
-- Root cause:
--   bg/session_health_worker.go 在 UPDATE session_summaries 时显式
--   列出 outcome 列（cmd/gateway/main.go:4753 注释提到 "批量计算并写入
--   session_summaries.health_score/grade/outcome"），但 sql/objects/tables/
--   session_summaries.sql canonical 定义**只有 health_score + health_grade**，
--   没有 outcome。base schema 启动时该表 inline CREATE 漏了 outcome 列。
--
-- Fix
-- ────
-- 在 startup/483 加 outcome 列，IF NOT EXISTS 幂等。注释对齐 cmd/gateway/main.go
-- 注释中提到的 "outcome" 语义。
--
-- Idempotent: 是（IF NOT EXISTS）
-- Down: 见 483_*.down.sql

BEGIN;

ALTER TABLE session_summaries
    ADD COLUMN IF NOT EXISTS outcome TEXT;

COMMIT;

-- POST_CONDITION: SELECT 1 FROM information_schema.columns WHERE table_name = 'session_summaries' AND column_name = 'outcome'
