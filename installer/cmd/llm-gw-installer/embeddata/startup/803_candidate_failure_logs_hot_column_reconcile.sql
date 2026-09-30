-- ===========================================================================
-- File:          sql/migrations/startup/803_candidate_failure_logs_hot_column_reconcile.sql
-- Migration:     803
-- Database:      llm_gateway
-- Purpose:       candidate_failure_logs 族补 extracted_upstream_status_code /
--                diagnosed_error_kind 两列（canonical 链 vs deploy 链列集对账）
--
-- Status:        active
-- Idempotent:    YES (ADD COLUMN IF NOT EXISTS，存量库零行为变化)
-- Dependencies:  392_candidate_failure_logs_monthly_partition.sql（hot 表）
--                617_candidate_failure_logs_hot_contract.sql（同族对账先例）
--
-- Background:
--   extracted_upstream_status_code / diagnosed_error_kind 自 300（父表）起
--   就存在于 canonical 链，但 hot 表侧从未有过任何 canonical SQL 迁移创建
--   它们——deploy 链（V359 血统）的库这两列一直在位（2026-10-01 真库实测：
--   生产 hot 表 21 列含两列，nullable 无默认），canonical 新装链却拿不到。
--   627 的 candidate_failure_logs_unified 视图按名字显式引用 hot 侧两列，
--   新装序列走到 627 即 42703 中断（2026-10-01 fresh-install e2e 实证，
--   392/535/617 接线后遗留的第二个基线缺口）。
--
--   本迁移把 deploy 链事实上的列集收编进 canonical 链（对齐 617 的
--   session_id/per_attempt_latency_ms 对账先例）：类型/可空性与生产实测
--   逐列一致（integer/text，nullable，无默认）。父表侧 ALTER 仅为防御性
--   no-op（baseline 已带两列）。
-- ===========================================================================

BEGIN;

ALTER TABLE IF EXISTS public.candidate_failure_logs
    ADD COLUMN IF NOT EXISTS extracted_upstream_status_code integer,
    ADD COLUMN IF NOT EXISTS diagnosed_error_kind text;

ALTER TABLE IF EXISTS public.candidate_failure_logs_hot
    ADD COLUMN IF NOT EXISTS extracted_upstream_status_code integer,
    ADD COLUMN IF NOT EXISTS diagnosed_error_kind text;

COMMIT;
