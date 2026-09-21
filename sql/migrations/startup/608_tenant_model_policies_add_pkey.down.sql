-- ===========================================================================
-- File:          sql/migrations/startup/608_tenant_model_policies_add_pkey.down.sql
-- Migration:     608 (DOWN)
-- Purpose:       回滚 608 —— 移除 tenant_model_policies 主键约束
--
-- Status:        idempotent (DROP CONSTRAINT IF EXISTS + 单事务)
--
-- ⚠️  WARNING: 回滚会恢复 608 修复前的漂移状态（id 无单列唯一性保证），
--   admin UPDATE ... WHERE id 与 dbx 门禁（DriftPKMismatch，blocking）都会
--   重新暴露在多行风险下。仅在明确的回滚决策下执行。
--   注：字典序上本文件先于 608 up 被 glob 匹配，在全新初始化流程里是
--   no-op（DROP IF EXISTS），随后 608 up 会重新补上主键，净状态正确。
-- ===========================================================================

\set ON_ERROR_STOP on

BEGIN;

ALTER TABLE public.tenant_model_policies
    DROP CONSTRAINT IF EXISTS tenant_model_policies_pkey;

COMMIT;
