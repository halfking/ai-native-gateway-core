-- Migration 524 down: restore the cmb NOTIFY trigger to pre-524 form
-- (drop context_window_override from the WHEN clause).
--
-- Drops the widened trigger and recreates the original predicate so
-- auto_route_refresh stops firing on context_window_override changes.

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
    )
    EXECUTE FUNCTION public.notify_auto_route_refresh();

COMMIT;
