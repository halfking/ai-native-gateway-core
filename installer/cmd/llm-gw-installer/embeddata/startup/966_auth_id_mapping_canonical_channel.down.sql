-- ===========================================================================
-- File:          966_auth_id_mapping_canonical_channel.down.sql
-- Migration:     966 (down)
-- Purpose:       回退 966——删除 auth_id_mapping 身份映射表与其触发器/函数。
--                回滚后果如实登记：auth_id_mapping 表内存放的 native 用户
--                身份映射（含 backfill 回填行）随表删除不可恢复；Casdoor
--                provider 的登录链路依赖该表，回退后启用 casdoor 的环境
--                会回到 966 之前的 42P01（auth_id_mapping 不存在）形态。
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Drop trigger first
DROP TRIGGER IF EXISTS auth_id_mapping_updated_at ON auth_id_mapping;

-- Drop function
DROP FUNCTION IF EXISTS update_auth_id_mapping_timestamp();

-- Drop indexes (will be dropped automatically with table, but explicit for clarity)
DROP INDEX IF EXISTS idx_auth_id_mapping_provider;
DROP INDEX IF EXISTS idx_auth_id_mapping_tenant;
DROP INDEX IF EXISTS idx_auth_id_mapping_username;
DROP INDEX IF EXISTS idx_auth_id_mapping_internal;
DROP INDEX IF EXISTS idx_auth_id_mapping_external;

-- Drop table
DROP TABLE IF EXISTS auth_id_mapping;

DELETE FROM public.schema_migrations WHERE version = '966';

COMMIT;
