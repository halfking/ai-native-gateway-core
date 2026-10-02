-- 801: bounded, lossless drain of session_turn_details_hot (supersedes 733's
-- promote function). 733's parent anti-join strands old hot rows whenever
-- backfill has already inserted the same request key. Its unreachable
-- ON CONFLICT DO UPDATE would also erase parent-only enrichment if enabled.
--
-- This migration changes only the function definition. Existing data is
-- drained by the regular promote worker after deployment; it does not run a
-- bulk DELETE or rewrite any existing partition during installation.
-- One call removes at most p_batch_size hot rows. The first phase removes an
-- old duplicate only when every non-NULL hot column except the surrogate id
-- strictly equals the corresponding parent column. Parent-only enrichment
-- survives. A true conflict remains in hot for explicit adjudication.
-- The second phase inserts a new key into the parent before deleting hot;
-- an insertion conflict leaves hot intact for the next reconciliation tick.

CREATE OR REPLACE FUNCTION public.promote_session_turn_details_hot_to_partition(
    p_retention INTERVAL DEFAULT '8 hours',
    p_batch_size INTEGER DEFAULT 5000
)
RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    v_deleted BIGINT := 0;
    v_inserted BIGINT := 0;
    v_partition_date DATE;
    v_parent_shape TEXT;
    v_hot_shape TEXT;
    v_cols TEXT;
    v_equal TEXT;
BEGIN
    IF p_retention IS NULL OR p_retention <= INTERVAL '0 seconds' THEN
        RAISE EXCEPTION 'p_retention must be a positive interval';
    END IF;
    IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 100000 THEN
        RAISE EXCEPTION 'p_batch_size must be between 1 and 100000';
    END IF;

    -- Keep the 733 fail-closed column contract: no newly added field may be
    -- silently omitted from either the equality check or the inserted row.
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_parent_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_turn_details'::regclass
       AND attnum > 0 AND NOT attisdropped;
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_hot_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_turn_details_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;
    IF v_parent_shape IS DISTINCT FROM v_hot_shape THEN
        RAISE EXCEPTION 'session_turn_details hot/parent column contract has drifted';
    END IF;

    SELECT string_agg(quote_ident(attname), ',' ORDER BY attnum),
           string_agg(format('(h.%1$I IS NULL OR h.%1$I IS NOT DISTINCT FROM p.%1$I)', attname),
                      ' AND ' ORDER BY attnum) FILTER (WHERE attname <> 'id')
      INTO v_cols, v_equal
      FROM pg_attribute
     WHERE attrelid = 'public.session_turn_details_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;

    PERFORM pg_advisory_xact_lock(
        hashtextextended('public.promote_session_turn_details_hot_to_partition', 0)
    );

    -- Typed IS NOT DISTINCT FROM compares arrays in order and JSONB values
    -- exactly. JSONB containment (@>) would incorrectly treat array subsets
    -- as equivalent, and to_jsonb(row) would conflate SQL NULL with JSON null.
    EXECUTE format(
        'WITH redundant AS (
            SELECT h.id
              FROM public.session_turn_details_hot h
              JOIN public.session_turn_details p
                ON p.tenant_id = h.tenant_id
               AND p.request_id = h.request_id
               AND p.partition_date = h.partition_date
             WHERE h.ts < statement_timestamp() - %L::interval
               AND %s
             ORDER BY h.ts, h.id
             LIMIT %s
             FOR UPDATE OF h SKIP LOCKED
        ), deleted AS (
            DELETE FROM public.session_turn_details_hot h
            USING redundant r
            WHERE h.id = r.id
            RETURNING 1
        )
        SELECT count(*) FROM deleted',
        p_retention, v_equal, p_batch_size)
    INTO v_deleted;

    IF v_deleted >= p_batch_size THEN
        RETURN v_deleted;
    END IF;

    -- There is no default partition. Ensure months for keys not already in
    -- parent before the insert. Conflict duplicates are excluded here and
    -- remain in hot; they must never be used to update a cold partition.
    -- Check all three parent unique keys: otherwise a stable session/turn
    -- or id conflict at the front of a batch would starve later good rows.
    FOR v_partition_date IN
        SELECT DISTINCT h.partition_date
          FROM public.session_turn_details_hot h
         WHERE h.ts < statement_timestamp() - p_retention
           AND NOT EXISTS (
               SELECT 1 FROM public.session_turn_details p
                WHERE p.tenant_id = h.tenant_id
                  AND p.request_id = h.request_id
                  AND p.partition_date = h.partition_date
           )
           AND NOT EXISTS (
               SELECT 1 FROM public.session_turn_details p
                WHERE p.session_id = h.session_id
                  AND p.turn_no = h.turn_no
                  AND p.partition_date = h.partition_date
           )
           AND NOT EXISTS (
               SELECT 1 FROM public.session_turn_details p
                WHERE p.id = h.id AND p.partition_date = h.partition_date
           )
    LOOP
        PERFORM public.ensure_session_turn_details_partition(v_partition_date);
    END LOOP;

    EXECUTE format(
        'WITH candidate AS (
            SELECT h.*
              FROM public.session_turn_details_hot h
             WHERE h.ts < statement_timestamp() - %L::interval
               AND NOT EXISTS (
                   SELECT 1 FROM public.session_turn_details p
                    WHERE p.tenant_id = h.tenant_id
                      AND p.request_id = h.request_id
                      AND p.partition_date = h.partition_date
               )
               AND NOT EXISTS (
                   SELECT 1 FROM public.session_turn_details p
                    WHERE p.session_id = h.session_id
                      AND p.turn_no = h.turn_no
                      AND p.partition_date = h.partition_date
               )
               AND NOT EXISTS (
                   SELECT 1 FROM public.session_turn_details p
                    WHERE p.id = h.id AND p.partition_date = h.partition_date
               )
             ORDER BY h.ts, h.id
             LIMIT %s
             FOR UPDATE SKIP LOCKED
        ), inserted AS (
            INSERT INTO public.session_turn_details (%s)
            SELECT %s FROM candidate
            ON CONFLICT DO NOTHING
            RETURNING tenant_id, request_id, partition_date
        ), deleted AS (
            DELETE FROM public.session_turn_details_hot h
            USING candidate c
            JOIN inserted i
              ON i.tenant_id = c.tenant_id
             AND i.request_id = c.request_id
             AND i.partition_date = c.partition_date
            WHERE h.id = c.id
            RETURNING 1
        )
        SELECT count(*) FROM deleted',
        p_retention, p_batch_size - v_deleted, v_cols, v_cols)
    INTO v_inserted;

    RETURN v_deleted + v_inserted;
END;
$$;

COMMENT ON FUNCTION public.promote_session_turn_details_hot_to_partition(INTERVAL, INTEGER) IS
    '759: drain at most one bounded batch per call; remove only redundant hot rows whose non-NULL values match parent, insert new keys before hot DELETE, preserve conflicts and parent enrichment.';
