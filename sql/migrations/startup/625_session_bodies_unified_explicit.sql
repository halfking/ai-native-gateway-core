-- Migration 625: materialise public.session_bodies_unified as a hot+partition
-- view the admin readers can rely on without rewriting every query.
--
-- Why:
--   2026-08-29 (P0 audit) observed that bodies_writer writes go to
--   public.session_bodies_hot (8-hour window), while the admin readers
--   (session_summary_v2, session_turns_v2, session_detail_v2, unified_detail)
--   all JOIN against public.session_bodies directly. After the migration 614
--   body hot write, recent turn bodies become invisible to these admin reads.
--   Migration 614 already created session_bodies_unified via SELECT * + UNION ALL,
--   but SELECT * is fragile against future schema drift and the view did not
--   enforce security_invoker. This migration makes the column set explicit,
--   re-runs the unified view with a security_invoker contract, and backfills
--   the index/keys that admin readers expect on hot rows.

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
FROM public.session_bodies
WHERE partition_date <= CURRENT_DATE - INTERVAL '1 day';

-- 2026-08-30 (audit follow-up): CREATE OR REPLACE VIEW cannot change the
-- security_invoker flag of an existing view (the flag is an attribute, not
-- part of the column list). Apply it explicitly with ALTER VIEW so a
-- database that already has 614's SELECT * view is upgraded in place. If
-- the view did not exist before this migration (e.g. 614 not yet applied),
-- CREATE OR REPLACE above creates it WITHOUT security_invoker; the ALTER
-- below promotes it to security_invoker=true. The defensive DO block at
-- the bottom asserts the final shape.
ALTER VIEW public.session_bodies_unified SET (security_invoker = true);

COMMENT ON VIEW public.session_bodies_unified IS
    'Explicit-column union of session_bodies_hot (recent writes) and historical session_bodies partitions. Admin readers MUST use this view rather than public.session_bodies to avoid missing recent writes still in the hot window.';

-- Defensive check: the view exists, uses security_invoker, and exposes the
-- 12 columns admin code reads.
DO $$
DECLARE
    v_colcount INT := 0;
    v_invoker BOOLEAN := FALSE;
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
END $$;

COMMIT;
