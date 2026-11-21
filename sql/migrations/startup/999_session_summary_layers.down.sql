-- ===========================================================================
-- File:          sql/migrations/startup/999_session_summary_layers.down.sql
-- Migration:     999 (rollback)
-- Database:      llm_gateway
-- Purpose:       回滚会话分层总结表创建
--
-- Date: 2026-11-21
-- Author: Kiro AI
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';
-- 删除表
DROP TABLE IF EXISTS public.session_summary_layers CASCADE;

-- 台账回退（与 up 的自登记对偶 —— 995/996 同款；down 只退自己引入的行）
DELETE FROM public.schema_migrations WHERE version = '999';

COMMIT;
