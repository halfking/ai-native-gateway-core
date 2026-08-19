-- 0003_shadow_user_audit.down.sql
DROP INDEX IF EXISTS idx_shadow_audit_action;
DROP INDEX IF EXISTS idx_shadow_audit_actor;
DROP INDEX IF EXISTS idx_shadow_audit_shadow;
DROP TABLE IF EXISTS shadow_audit;