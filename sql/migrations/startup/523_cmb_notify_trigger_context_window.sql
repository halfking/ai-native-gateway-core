-- Migration 523: make the auto_route NOTIFY trigger fire on context_window_override
--
-- 522 added credential_model_bindings.context_window_override, which feeds the
-- runtime candidate SQL (provider/client.go) and therefore the compression
-- trigger thresholds (transformation.ThresholdBytes, the session compressor's
-- TOKEN trigger, and the 4xx smart-window recovery cut point).
--
-- But the AFTER UPDATE trigger trg_notify_auto_route_cmb_update only watched
-- routing/availability columns, NOT context_window_override. So an admin
-- calibrating a binding's context window:
--   - invalidated the in-process candCache (handler calls
--     InvalidateAvailableModelsCache + we add invalidateRoutingCaches below),
--   - but did NOT NOTIFY the auto_route_refresh channel,
-- leaving every OTHER gateway instance's candCache with the stale window. The
-- next request on another instance trimmed/skipped compression off the old
-- value — exactly the "compression trigger timing + multi-layer cache" hazard
-- the review flagged. This migration widens the trigger's WHEN clause so an
-- override change naturally fans out a NOTIFY to all instances.
--
-- Idempotent: DROP + CREATE. The CREATE is kept in lockstep with the baseline
-- sql/objects/triggers/credential_model_bindings_trg_notify_auto_route_cmb_update.sql.

BEGIN;

DROP TRIGGER IF EXISTS trg_notify_auto_route_cmb_update ON public.credential_model_bindings;

CREATE TRIGGER trg_notify_auto_route_cmb_update
    AFTER UPDATE ON public.credential_model_bindings
    FOR EACH ROW
    WHEN (
        old.available IS DISTINCT FROM new.available
        OR old.unavailable_reason IS DISTINCT FROM new.unavailable_reason
        OR old.unavailable_at IS DISTINCT FROM new.unavailable_at
        OR old.routing_tier IS DISTINCT FROM new.routing_tier
        OR old.weight IS DISTINCT FROM new.weight
        OR old.manual_priority IS DISTINCT FROM new.manual_priority
        OR old.active_sessions IS DISTINCT FROM new.active_sessions
        OR old.consecutive_failures IS DISTINCT FROM new.consecutive_failures
        OR old.context_window_override IS DISTINCT FROM new.context_window_override
    )
    EXECUTE FUNCTION public.notify_auto_route_refresh();

COMMIT;
