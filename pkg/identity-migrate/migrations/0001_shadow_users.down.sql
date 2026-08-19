-- 0001_shadow_users.down.sql
DROP INDEX IF EXISTS idx_shadow_users_status;
DROP INDEX IF EXISTS idx_shadow_users_email;
DROP INDEX IF EXISTS idx_shadow_users_canonical;
DROP TABLE IF EXISTS shadow_users;