-- Rollback for migration 638: restore migration 626's promote function body
-- (no retention/batch guards), copied verbatim from
-- 626_session_bodies_hot_promote_reconcile.sql.
--
-- WARNING: this re-enables the unguarded retention path — calling the
-- function with retention_window <= 0 drains every hot row out of the 8-hour
-- window. 626's idempotent conflict semantics are preserved.

BEGIN;

CREATE OR REPLACE FUNCTION public.promote_session_bodies_hot_to_partition(
    retention_window interval DEFAULT '8 hours',
    batch_size integer DEFAULT 5000
)
RETURNS TABLE(moved_count bigint) AS $$
DECLARE
    cutoff_ts timestamptz;
BEGIN
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
        -- Delete only rows that were actually inserted above. Matching on the
        -- full hot-table primary key (id, partition_date) keeps rows whose
        -- INSERT was skipped by ON CONFLICT DO NOTHING safely in the hot
        -- table for the next cycle instead of silently dropping them.
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

COMMIT;
