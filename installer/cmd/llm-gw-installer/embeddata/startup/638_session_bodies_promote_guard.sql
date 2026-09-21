-- Migration 638: add retention/batch guards to the session_bodies promote.
--
-- Why (2026-09-01, 24h-audit round 2, P1):
--   promote_candidate_failure_logs_hot_to_partition (migration 628) rejects
--   NULL / non-positive retention with RAISE EXCEPTION, but
--   promote_session_bodies_hot_to_partition (615, reinstalled by 626) has no
--   such guard. With retention_window <= 0 the cutoff becomes now() or later
--   and the promote would drain EVERY hot row — including rows written a
--   second ago — into the partition store, defeating the 8-hour hot window
--   the writer and admin hot reads rely on. A NULL or <=0 batch_size is
--   equally nonsensical (LIMIT NULL / LIMIT 0).
--
-- 615 is checksum-frozen in deployed installations and 626 already set the
-- precedent of replacing the function in a later migration, so this
-- migration reinstalls the 626 body (identical idempotent semantics: ON
-- CONFLICT (id, partition_date) DO NOTHING, delete only rows actually
-- inserted) with the guards added, mirroring 628's guard pattern.

BEGIN;

\set ON_ERROR_STOP on

CREATE OR REPLACE FUNCTION public.promote_session_bodies_hot_to_partition(
    retention_window interval DEFAULT '8 hours',
    batch_size integer DEFAULT 5000
)
RETURNS TABLE(moved_count bigint) AS $$
DECLARE
    cutoff_ts timestamptz;
BEGIN
    -- Guards (mirror migration 628's promote_candidate_failure_logs pattern):
    -- a NULL / non-positive retention would drain the whole hot table and
    -- break the 8-hour hot window; a NULL / non-positive batch size makes
    -- LIMIT meaningless.
    IF retention_window IS NULL OR retention_window <= interval '0 seconds' THEN
        RAISE EXCEPTION 'retention_window must be positive';
    END IF;
    IF batch_size IS NULL OR batch_size < 1 THEN
        RAISE EXCEPTION 'batch_size must be >= 1';
    END IF;

    cutoff_ts := now() - retention_window;

    IF NOT pg_try_advisory_xact_lock(hashtext('public.promote_session_bodies_hot_to_partition')) THEN
        RETURN QUERY SELECT 0::bigint;
        RETURN;
    END IF;

    WITH to_move AS (
        SELECT id, session_id, turn_no, tenant_id, request_id, ts,
               request_delta, response_delta, outbound_body,
               request_attachments, response_attachments, partition_date
        FROM public.session_bodies_hot
        WHERE ts < cutoff_ts
        ORDER BY ts
        LIMIT batch_size
        FOR UPDATE SKIP LOCKED
    ),
    inserted AS (
        INSERT INTO public.session_bodies (
            id, session_id, turn_no, tenant_id, request_id, ts,
            request_delta, response_delta, outbound_body,
            request_attachments, response_attachments, partition_date
        )
        SELECT id, session_id, turn_no, tenant_id, request_id, ts,
               request_delta, response_delta, outbound_body,
               request_attachments, response_attachments, partition_date
        FROM to_move
        ON CONFLICT (id, partition_date) DO NOTHING
        RETURNING id, partition_date
    ),
    deleted AS (
        -- Delete only rows that were actually inserted above (626 semantics).
        -- Matching on the full hot-table primary key (id, partition_date)
        -- keeps rows whose INSERT was skipped by ON CONFLICT DO NOTHING
        -- safely in the hot table for the next cycle.
        DELETE FROM public.session_bodies_hot h
        USING inserted i
        WHERE h.id = i.id
          AND h.partition_date = i.partition_date
        RETURNING 1
    )
    SELECT count(*) INTO moved_count FROM deleted;

    RETURN QUERY SELECT moved_count;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION public.promote_session_bodies_hot_to_partition IS
    'Atomically move old rows from session_bodies_hot to monthly partitions. Called by PartitionManager. Rejects NULL/non-positive retention_window or batch_size (migration 638).';

-- Post-condition: the guarded function signature exists.
DO $$
BEGIN
    IF to_regprocedure('public.promote_session_bodies_hot_to_partition(interval,integer)') IS NULL THEN
        RAISE EXCEPTION 'session_bodies promote guard post-condition failed';
    END IF;
END
$$;

COMMIT;
