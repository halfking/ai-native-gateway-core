-- ===========================================================================
-- File:          sql/migrations/startup/983_request_metrics.down.sql
-- Migration:     983 (回滚)
-- Database:      llm_gateway
-- Purpose:       回滚 request_metrics 表
--
-- Status:        active
-- Idempotent:    YES (DROP TABLE IF EXISTS)
--
-- Date: 2026-11-23
-- Author: Kiro AI
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

-- ============================================================
-- 1. 删除索引（分区表的索引会随表自动删除，但显式删除更清晰）
-- ============================================================

DROP INDEX IF EXISTS public.idx_request_metrics_tenant_time;
DROP INDEX IF EXISTS public.idx_request_metrics_tenant_billing_date;
DROP INDEX IF EXISTS public.idx_request_metrics_gateway_node_time;
DROP INDEX IF EXISTS public.idx_request_metrics_provider_time;
DROP INDEX IF EXISTS public.idx_request_metrics_session_time;
DROP INDEX IF EXISTS public.idx_request_metrics_traffic_time;
DROP INDEX IF EXISTS public.idx_request_metrics_payer_time;
DROP INDEX IF EXISTS public.idx_request_metrics_team_time;
DROP INDEX IF EXISTS public.idx_request_metrics_status_time;
DROP INDEX IF EXISTS public.idx_request_metrics_modality_time;
DROP INDEX IF EXISTS public.idx_request_metrics_protocol_time;

-- ============================================================
-- 2. 删除分区表（会自动删除所有分区）
-- ============================================================

DROP TABLE IF EXISTS public.request_metrics CASCADE;

-- ============================================================
-- 3. 台账回退（与 up 的自登记对偶）
-- ============================================================
-- down 只退自己引入的行
DELETE FROM public.schema_migrations WHERE version = '983';

COMMIT;
