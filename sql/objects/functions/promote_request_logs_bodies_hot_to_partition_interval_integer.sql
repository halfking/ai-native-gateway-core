--
-- Name: promote_request_logs_bodies_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_request_logs_bodies_hot_to_partition(
  p_retention interval DEFAULT '24 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE
  v_processed bigint := 0;
  v_ttl_days int := 7;
  month_rec record;
BEGIN
  -- 698: group months under Asia/Shanghai so boundary rows land in the
  -- same month group the 694-pinned ensure_* target was created for.
  SET LOCAL TIME ZONE 'Asia/Shanghai';
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;

  -- Phase 1 (unchanged from 528): delete rows older than the configured body
  -- TTL so a stale backlog cannot block promote on dropped partitions (23514).
  SELECT CASE jsonb_typeof(value)
           WHEN 'number' THEN value::text::int
           WHEN 'string' THEN trim(both '"' from value::text)::int
           ELSE 7
         END
    INTO v_ttl_days
    FROM settings_kv
   WHERE key = 'lifecycle.request_logs_bodies_ttl_days'
     AND scope = 'platform'
   LIMIT 1;

  v_ttl_days := GREATEST(COALESCE(v_ttl_days, 7), 1);

  WITH expired_batch AS (
    SELECT request_id
      FROM public.request_logs_bodies_hot
     WHERE ts < now() - make_interval(days => v_ttl_days)
       AND ts < now() - p_retention
     ORDER BY ts
     LIMIT p_batch_size
  ),
  expired_deleted AS (
    DELETE FROM public.request_logs_bodies_hot
     WHERE request_id IN (SELECT request_id FROM expired_batch)
    RETURNING request_id
  )
  SELECT count(*) INTO v_processed FROM expired_deleted;

  IF v_processed > 0 THEN
    RETURN v_processed;
  END IF;

  -- Phase 2: atomic promote (528 was already one statement but had no guards,
  -- no pre-ensure, no SKIP LOCKED and used RETURNING *).
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', ts) AS month_start
    FROM public.request_logs_bodies_hot
    WHERE ts < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_request_logs_bodies_partition(month_rec.month_start);
  END LOOP;

  WITH batch AS (
    SELECT request_id FROM public.request_logs_bodies_hot
    WHERE ts < now() - p_retention
    ORDER BY ts, request_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.request_logs_bodies_hot h USING batch b
    WHERE h.request_id = b.request_id
    RETURNING h.request_id, h.ts, h.request_body, h.outbound_body, h.response_body
  ), inserted AS (
    INSERT INTO public.request_logs_bodies (
      request_id, ts, request_body, outbound_body, response_body)
    SELECT request_id, ts, request_body, outbound_body, response_body
    FROM moved_rows
    RETURNING request_id
  ) SELECT count(*) INTO v_processed FROM inserted;
  RETURN v_processed;
END;
$$;
