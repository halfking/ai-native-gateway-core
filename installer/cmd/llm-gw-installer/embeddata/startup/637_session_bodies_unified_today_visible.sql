-- Migration 637: make session_bodies_unified cover today's promoted rows.
--
-- Why (2026-09-01, 24h-audit round 2, P0):
--   Migration 625's view kept a partition-branch filter
--     WHERE partition_date <= CURRENT_DATE - INTERVAL '1 day'
--   inherited from 614's SELECT-* union. bodies_writer (bodies_writer.go)
--   only writes session_bodies_hot with partition_date = the write day, and
--   promote (promote_session_bodies_hot_to_partition, 615/626) inserts the
--   row into public.session_bodies with its ORIGINAL partition_date and then
--   deletes the hot row in the same transaction. A row written today and
--   promoted today therefore lands in a partition whose partition_date is
--   CURRENT_DATE, which the view's partition branch excludes — from the
--   moment promote runs until midnight the row is invisible to every
--   session_bodies_unified reader. GetLatestBodies (the request_delta
--   deduplication baseline in domains/session/v2) reads the parent table, but
--   admin readers on the unified view lost the row, breaking the
--   read/write/promote data-closure loop.
--
-- Why removing the filter is safe (no duplication risk):
--   promote is a MOVE, not a copy — the hot row is deleted in the same
--   transaction as the parent insert, so at any snapshot a row lives in
--   exactly one of the two branches. The filter was a leftover optimisation
--   from when the view was assumed to read hot + "historical" partitions,
--   but a row promoted the same day it was written is neither historical
--   nor still hot. Keeping the UNION ALL structure with the hot branch
--   unfiltered (it always was) plus the partition branch unfiltered yields
--   exactly one copy per row.
--
-- Everything else from 625 is retained: explicit 12-column list, hot-first
-- UNION ALL, security_invoker=true via ALTER VIEW (CREATE OR REPLACE cannot
-- change reloptions), view comment, and the defensive DO block asserting the
-- final shape.

BEGIN;

\set ON_ERROR_STOP on

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
FROM public.session_bodies;

-- CREATE OR REPLACE VIEW cannot change the security_invoker flag of an
-- existing view (the flag is an attribute, not part of the column list), so
-- re-assert it explicitly as 625/626 did.
ALTER VIEW public.session_bodies_unified SET (security_invoker = true);

COMMENT ON VIEW public.session_bodies_unified IS
    'Explicit-column union of session_bodies_hot (recent writes) and historical session_bodies partitions. Admin readers MUST use this view rather than public.session_bodies to avoid missing recent writes still in the hot window. Since migration 637 the partition branch is unfiltered: rows promoted on their write day stay visible (promote is a move, not a copy, so branches cannot overlap).';

-- Defensive check: the view exists, uses security_invoker, exposes the 12
-- columns admin code reads, and no longer carries the same-day visibility
-- hole from 625.
DO $$
DECLARE
    v_colcount INT := 0;
    v_invoker BOOLEAN := FALSE;
    v_definition TEXT := '';
    v_partition_branch TEXT := '';
BEGIN
    SELECT COUNT(*) INTO v_colcount
    FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'session_bodies_unified';

    IF v_colcount <> 12 THEN
        RAISE EXCEPTION 'session_bodies_unified must expose 12 columns, got %', v_colcount;
    END IF;

    SELECT 'security_invoker=true' = ANY(reloptions) INTO v_invoker
    FROM pg_class
    WHERE oid = 'public.session_bodies_unified'::regclass;

    IF NOT v_invoker THEN
        RAISE EXCEPTION 'session_bodies_unified must use security_invoker=true';
    END IF;

    SELECT pg_get_viewdef('public.session_bodies_unified'::regclass, true)
      INTO v_definition;
    v_partition_branch := substring(v_definition from 'UNION ALL(.*)$');

    IF v_partition_branch IS NULL OR v_partition_branch = '' THEN
        RAISE EXCEPTION 'session_bodies_unified must remain a hot UNION ALL partition view';
    END IF;

    IF v_partition_branch LIKE '%CURRENT_DATE - INTERVAL%' THEN
        RAISE EXCEPTION 'session_bodies_unified partition branch must not filter partition_date (637 removed the same-day visibility hole)';
    END IF;
END $$;

COMMIT;
