-- Migration 626: repair Session V2 body promotion and view RLS semantics.
--
-- 615 is checksum-frozen in deployed installations. Replacing the function here
-- makes conflict reconciliation apply to both fresh and already-upgraded DBs.
-- 625's explicit view body is retained; ALTER VIEW makes security_invoker
-- deterministic when upgrading an existing 614 SELECT-* view.

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
        RETURNING id
    ),
    deleted AS (
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

ALTER VIEW public.session_bodies_unified SET (security_invoker = true);

COMMIT;
