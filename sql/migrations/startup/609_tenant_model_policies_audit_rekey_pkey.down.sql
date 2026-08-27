-- ===========================================================================
-- File:          sql/migrations/startup/609_tenant_model_policies_audit_rekey_pkey.down.sql
-- Migration:     609 (DOWN)
-- Purpose:       回滚 609 的约束部分 —— 移除 tenant_model_policies_audit 主键
--
-- Status:        idempotent (DROP CONSTRAINT IF EXISTS + 单事务)
--
-- ⚠️  WARNING:
--   1. 只回滚 DDL（DROP CONSTRAINT）；609 的 re-key 数据变更不可逆
--      （原始重复 id 未保留）。
--   2. 回滚后 audit 表重新暴露在 id 无唯一性保证的漂移状态下。
--   注：字典序上本文件先于 609 up 被 glob 匹配，在全新初始化流程里对
--   无约束状态是 no-op（DROP IF EXISTS）。
-- ===========================================================================

\set ON_ERROR_STOP on

BEGIN;

ALTER TABLE public.tenant_model_policies_audit
    DROP CONSTRAINT IF EXISTS tenant_model_policies_audit_pkey;

COMMIT;
