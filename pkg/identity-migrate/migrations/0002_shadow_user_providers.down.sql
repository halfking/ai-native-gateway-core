-- 0002_shadow_user_providers.down.sql
DROP INDEX IF EXISTS idx_shadow_user_providers_last_seen;
DROP INDEX IF EXISTS idx_shadow_user_providers_external;
DROP INDEX IF EXISTS idx_shadow_user_providers_tenant_subject;
DROP INDEX IF EXISTS idx_shadow_user_providers_shadow;
DROP TABLE IF EXISTS shadow_user_providers;