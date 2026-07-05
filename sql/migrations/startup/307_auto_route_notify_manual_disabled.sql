-- Migration 307: Extend auto_route_refresh trigger to watch manual_disabled
--             + add the same trigger on providers table
--
-- Purpose (2026-06-28):
--   Fix the gap where toggling credentials.manual_disabled or
--   providers.manual_disabled did NOT wake the AutoRouteRealtimeListener.
--   The original trigger (2026-06-15-auto-route-mode-realtime-trigger.sql)
--   watches credentials.{status,availability_state,quota_state,
--   circuit_state,concurrency_limit,lifecycle_status} but omits
--   manual_disabled, and there is no trigger on providers at all.
--   Symptom: an admin correcting a credential's status via the
--   clear-manual-disabled / set-manual-disabled endpoint saw no auto-route
--   refresh for up to 5 min; the restored credential stayed invisible to
--   the autoroute until the periodic refresh fired.
--
-- Design notes:
--   - Re-define the existing trg_notify_auto_route_creds to add
--     manual_disabled to its UPDATE OF column list. WHEN (OLD.* IS
--     DISTINCT FROM NEW.*) guards against false wakeups on no-op updates.
--   - Add a new trg_notify_auto_route_providers on providers that fires
--     on UPDATE OF enabled, manual_disabled. providers has no other
--     auto-route-impacting column today, so two columns is enough.
--   - Trigger function notify_auto_route_refresh() is unchanged — the
--     TG_TABLE_NAME dispatch already handles "credentials" and "api_keys";
--     extend it to also cover "providers" so the payload's entity_id is
--     populated correctly (used by /api/admin/auto-route/decisions and
--     observability).
--
-- Date: 2026-06-28

BEGIN;

-- ── 1. Extend trigger function to recognize the providers table ─────────────
CREATE OR REPLACE FUNCTION notify_auto_route_refresh()
RETURNS TRIGGER AS $$
DECLARE
    entity_id text := '';
BEGIN
    -- Dispatch table → entity_id column. credential_model_bindings uses
    -- credential_id; credentials/api_keys/providers use id.
    IF TG_TABLE_NAME = 'credential_model_bindings' THEN
        entity_id := COALESCE(NEW.credential_id, OLD.credential_id)::text;
    ELSIF TG_TABLE_NAME IN ('credentials', 'api_keys', 'providers') THEN
        entity_id := COALESCE(NEW.id, OLD.id)::text;
    END IF;

    PERFORM pg_notify('auto_route_refresh',
        TG_TABLE_NAME || ':' || TG_OP || ':' || entity_id);
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

-- ── 2. Re-create credentials trigger with manual_disabled added ────────────
DROP TRIGGER IF EXISTS trg_notify_auto_route_creds ON credentials;
CREATE TRIGGER trg_notify_auto_route_creds
AFTER UPDATE OF
    status, availability_state, quota_state, circuit_state,
    concurrency_limit, lifecycle_status,
    manual_disabled                   -- ← added 2026-06-28
ON credentials
FOR EACH ROW
WHEN (OLD.* IS DISTINCT FROM NEW.*)
EXECUTE FUNCTION notify_auto_route_refresh();

-- ── 3. New trigger on providers ────────────────────────────────────────────
DROP TRIGGER IF EXISTS trg_notify_auto_route_providers ON providers;
CREATE TRIGGER trg_notify_auto_route_providers
AFTER UPDATE OF
    enabled,
    manual_disabled                   -- ← entire providers-trigger story
ON providers
FOR EACH ROW
WHEN (OLD.* IS DISTINCT FROM NEW.*)
EXECUTE FUNCTION notify_auto_route_refresh();

COMMIT;