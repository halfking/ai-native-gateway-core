-- Down migration for 1005_user_roles_array
BEGIN;
ALTER TABLE users DROP COLUMN IF EXISTS roles;
COMMIT;
