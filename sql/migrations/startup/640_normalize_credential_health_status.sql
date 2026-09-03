-- Migration 640: normalize legacy credential health statuses.
-- health_status is constrained to unknown/healthy/warning/unreachable; older
-- workers wrote error/degraded, so normalize those rows before the constraint
-- is relied upon by the unified credential status projection.
BEGIN;

UPDATE public.credentials
SET health_status = CASE health_status
    WHEN 'error' THEN 'unreachable'
    WHEN 'degraded' THEN 'warning'
    ELSE health_status
END
WHERE health_status IN ('error', 'degraded');

COMMIT;
