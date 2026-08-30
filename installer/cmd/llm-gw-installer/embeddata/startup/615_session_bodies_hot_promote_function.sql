-- Migration: 615_session_bodies_hot_promote_function
-- Purpose: Create promote function for session_bodies_hot table
-- Related: Migration 614 (session_bodies_hot table creation)
-- Date: 2026-08-29

-- Function to promote old rows from session_bodies_hot to monthly partitions
-- Called by PartitionManager every hour to maintain 8-hour hot window
CREATE OR REPLACE FUNCTION public.promote_session_bodies_hot_to_partition(
    retention_window interval DEFAULT '8 hours',
    batch_size integer DEFAULT 5000
)
RETURNS TABLE(moved_count bigint) AS $$
DECLARE
    cutoff_ts timestamptz;
BEGIN
    cutoff_ts := now() - retention_window;

    -- Transaction-scoped advisory lock so only one promote run can mutate
    -- session_bodies_hot at a time, even across multiple instances.
    PERFORM pg_advisory_xact_lock(hashtext('session_bodies_hot_promote'));

    -- Move rows older than retention window to partition table
    -- Use advisory lock to prevent concurrent promote from same/different instance
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
        DELETE FROM public.session_bodies_hot
        WHERE id IN (SELECT id FROM inserted)
        RETURNING 1
    )
    SELECT count(*) INTO moved_count FROM deleted;
    
    RETURN QUERY SELECT moved_count;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION public.promote_session_bodies_hot_to_partition IS
    'Atomically move old rows from session_bodies_hot to monthly partitions. Called by PartitionManager.';
