-- Rollback for migration 637: restore migration 625's view body, i.e. the
-- explicit 12-column hot UNION ALL partition view WITH the partition-branch
-- filter `partition_date <= CURRENT_DATE - INTERVAL '1 day'`.
--
-- WARNING: rolling back re-introduces the same-day visibility hole that 637
-- fixed — a row written and promoted on the same day disappears from this
-- view until midnight. Only use this downgrade if a consumer genuinely
-- depends on the filtered shape; readers of the unified view (admin detail /
-- summary / GetLatestBodies dedup baseline) are expected to be migrated to
-- the unfiltered view first.
--
-- The view is never dropped: five Go readers depend on it existing.

BEGIN;

CREATE OR REPLACE VIEW public.session_bodies_unified AS
SELECT
    id,
    session_id,
    turn_no,
    tenant_id,
    request_id,
    request_delta,
    response_delta,
    outbound_body,
    request_attachments,
    response_attachments,
    ts,
    partition_date
FROM public.session_bodies_hot
UNION ALL
SELECT
    id,
    session_id,
    turn_no,
    tenant_id,
    request_id,
    request_delta,
    response_delta,
    outbound_body,
    request_attachments,
    response_attachments,
    ts,
    partition_date
FROM public.session_bodies
WHERE partition_date <= CURRENT_DATE - INTERVAL '1 day';

-- CREATE OR REPLACE preserves reloptions; keep 625/626/637's security
-- contract rather than resetting it (downgrading the flag would silently
-- change RLS semantics for admin readers).
ALTER VIEW public.session_bodies_unified SET (security_invoker = true);

COMMIT;
