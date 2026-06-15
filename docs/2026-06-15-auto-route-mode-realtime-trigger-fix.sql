-- 2026-06-15-auto-route-mode-realtime-trigger-fix.sql
-- Fix notify_auto_route_refresh() referencing credential_id on tables that use id.
--
-- Root cause: api_keys UPDATE on rate_limit_rpm fired trg_notify_auto_route_apikeys,
-- but the shared trigger function accessed NEW.credential_id (only exists on
-- credential_model_bindings). credentials and api_keys both use id.
--
-- Symptom: PATCH /api/keys/{id}/limits → 500 {"error":{"detail":"update failed"}}

BEGIN;

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
