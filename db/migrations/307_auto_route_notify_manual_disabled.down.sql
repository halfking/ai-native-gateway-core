-- Migration 307 down: Restore the original 2026-06-15 trigger definition.
--
-- Reverses:
--   - manual_disabled removed from credentials trigger's UPDATE OF list
--   - providers trigger dropped
--   - notify_auto_route_refresh() reverted to dispatch only
--     credentials / api_keys / credential_model_bindings (no providers)

BEGIN;

DROP TRIGGER IF EXISTS trg_notify_auto_route_providers ON providers;

DROP TRIGGER IF EXISTS trg_notify_auto_route_creds ON credentials;
CREATE TRIGGER trg_notify_auto_route_creds
AFTER UPDATE OF
    status, availability_state, quota_state, circuit_state,
    concurrency_limit, lifecycle_status
ON credentials
FOR EACH ROW
WHEN (OLD.* IS DISTINCT FROM NEW.*)
EXECUTE FUNCTION notify_auto_route_refresh();

CREATE OR REPLACE FUNCTION notify_auto_route_refresh()
RETURNS TRIGGER AS $$
DECLARE
    entity_id text := '';
BEGIN
    IF TG_TABLE_NAME = 'credential_model_bindings' THEN
        entity_id := COALESCE(NEW.credential_id, OLD.credential_id)::text;
    ELSIF TG_TABLE_NAME IN ('credentials', 'api_keys') THEN
        entity_id := COALESCE(NEW.id, OLD.id)::text;
    END IF;

    PERFORM pg_notify('auto_route_refresh',
        TG_TABLE_NAME || ':' || TG_OP || entity_id);
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

COMMIT;