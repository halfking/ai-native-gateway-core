-- ===========================================================================
-- File:          sql/migrations/startup/968_plugin_config.down.sql
-- Migration:     968 (down)
-- Purpose:       回退 968——删除 plugin_config 表（插件 provider 选择持久化）。
--
-- 回滚后果如实登记：
--   · plugin_config 表内保存的是**运维在 admin 面上选的 provider**
--     （auth/user_manager/tenant_manager/approval 四列）⇒ 随表删除不可恢复。
--   · 删除后 cmd/gateway/plugin_init.go 的 loadConfigFromDatabase 查询报
--     42P01 ⇒ 代码走 slog.Debug 分支 ⇒ **静默回落 default（全部 native）**
--     ⇒ 运维此前切到 casdoor 的选择在重启后丢失。
--   · ⚠️ down 会执行 DELETE FROM public.schema_migrations WHERE version='968'
--     ⇒ 回滚不是幂等 no-op，需按部署序列重放968 才能恢复记账。
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

DROP INDEX IF EXISTS public.idx_plugin_config_updated;
DROP TABLE IF EXISTS public.plugin_config;

DELETE FROM public.schema_migrations WHERE version = '968';

COMMIT;
