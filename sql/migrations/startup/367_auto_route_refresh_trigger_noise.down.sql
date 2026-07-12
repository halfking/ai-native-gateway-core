BEGIN;

DROP TRIGGER IF EXISTS trg_notify_auto_route_cmb ON credential_model_bindings;
DROP TRIGGER IF EXISTS trg_notify_auto_route_cmb_insert_delete ON credential_model_bindings;
DROP TRIGGER IF EXISTS trg_notify_auto_route_cmb_update ON credential_model_bindings;

CREATE TRIGGER trg_notify_auto_route_cmb
AFTER INSERT OR DELETE OR UPDATE ON credential_model_bindings
FOR EACH ROW
EXECUTE FUNCTION notify_auto_route_refresh();

COMMIT;
