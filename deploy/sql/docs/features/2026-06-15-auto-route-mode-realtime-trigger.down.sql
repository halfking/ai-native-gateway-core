-- 2026-06-15-auto-route-mode-realtime-trigger.down.sql
-- 回滚 realtime LISTEN/NOTIFY triggers
BEGIN;

DROP TRIGGER IF EXISTS trg_notify_auto_route_cmb ON credential_model_bindings;
DROP TRIGGER IF EXISTS trg_notify_auto_route_creds ON credentials;
DROP TRIGGER IF EXISTS trg_notify_auto_route_apikeys ON api_keys;

DROP FUNCTION IF EXISTS notify_auto_route_refresh();

COMMIT;