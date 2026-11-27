-- 回滚 1004（三级模型过滤）：见 1004_three_tier_model_policies.sql 头注释。
-- 注意顺序：先 drop 依赖新表的视图/函数/审计表，再 drop 新表与新列。

SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

DROP TRIGGER IF EXISTS user_model_policies_audit_trg ON user_model_policies;
DROP FUNCTION IF EXISTS user_model_policies_audit_fn();
DROP TABLE IF EXISTS user_model_policies_audit;
DROP VIEW IF EXISTS user_model_policies_active;
DROP TABLE IF EXISTS user_model_policies;

DROP TRIGGER IF EXISTS org_model_policies_audit_trg ON org_model_policies;
DROP FUNCTION IF EXISTS org_model_policies_audit_fn();
DROP TABLE IF EXISTS org_model_policies_audit;
DROP VIEW IF EXISTS org_model_policies_active;
DROP TABLE IF EXISTS org_model_policies;

-- tenant_model_policies_active 视图还原为 024 形状（无 mode 列）
CREATE OR REPLACE VIEW tenant_model_policies_active AS
    SELECT id, tenant_id, canonical_name, reason, created_by,
           created_at, updated_at
    FROM tenant_model_policies
    WHERE deleted_at IS NULL;

ALTER TABLE tenant_model_policies DROP COLUMN IF EXISTS mode;

COMMIT;
