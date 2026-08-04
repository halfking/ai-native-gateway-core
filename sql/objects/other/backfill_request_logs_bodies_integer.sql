--
-- Name: backfill_request_logs_bodies(integer); Type: PROCEDURE; Schema: public; Owner: -
--

CREATE PROCEDURE public.backfill_request_logs_bodies(IN p_batch integer DEFAULT 200)
    LANGUAGE plpgsql
    AS $$
DECLARE
    rec record;
    inserted int := 0;
BEGIN
    FOR rec IN
        SELECT id
        FROM request_logs
        WHERE (request_body IS NOT NULL OR outbound_body IS NOT NULL OR response_body IS NOT NULL)
          AND NOT EXISTS (
              SELECT 1 FROM request_logs_bodies b
              WHERE b.request_id = request_logs.request_id AND b.ts = request_logs.ts)
        ORDER BY id
        LIMIT p_batch
    LOOP
        INSERT INTO request_logs_bodies (request_id, ts, request_body, outbound_body, response_body)
        SELECT request_id, ts, request_body, outbound_body, response_body
        FROM request_logs
        WHERE id = rec.id;
        inserted := inserted + 1;
    END LOOP;

    RAISE NOTICE 'backfill_request_logs_bodies: inserted % rows (batch %)',
        inserted, p_batch;
END;
$$;

