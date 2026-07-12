-- 367: avoid rebuilding the auto-route index for no-op/telemetry binding updates.
-- The previous trigger fired for every UPDATE on credential_model_bindings,
-- including health counters and timestamps that do not change routing.

BEGIN;

DROP TRIGGER IF EXISTS trg_notify_auto_route_cmb ON credential_model_bindings;
DROP TRIGGER IF EXISTS trg_notify_auto_route_cmb_insert_delete ON credential_model_bindings;
DROP TRIGGER IF EXISTS trg_notify_auto_route_cmb_update ON credential_model_bindings;

CREATE TRIGGER trg_notify_auto_route_cmb_insert_delete
AFTER INSERT OR DELETE ON credential_model_bindings
FOR EACH ROW
EXECUTE FUNCTION notify_auto_route_refresh();

CREATE TRIGGER trg_notify_auto_route_cmb_update
AFTER UPDATE ON credential_model_bindings
FOR EACH ROW
WHEN (
    OLD.available IS DISTINCT FROM NEW.available OR
    OLD.unavailable_reason IS DISTINCT FROM NEW.unavailable_reason OR
    OLD.unavailable_at IS DISTINCT FROM NEW.unavailable_at OR
    OLD.routing_tier IS DISTINCT FROM NEW.routing_tier OR
    OLD.weight IS DISTINCT FROM NEW.weight OR
    OLD.manual_priority IS DISTINCT FROM NEW.manual_priority OR
    OLD.active_sessions IS DISTINCT FROM NEW.active_sessions OR
    OLD.consecutive_failures IS DISTINCT FROM NEW.consecutive_failures
)
EXECUTE FUNCTION notify_auto_route_refresh();

COMMIT;
