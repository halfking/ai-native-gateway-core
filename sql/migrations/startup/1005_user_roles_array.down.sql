-- Down migration for 1005_user_roles_array
BEGIN;
ALTER TABLE users DROP COLUMN IF EXISTS roles;

-- ★ 台账自登记的回滚半边（与 up 件的 INSERT 对称；633.down 同款）。
DELETE FROM public.schema_migrations WHERE version = '1005';

COMMIT;
