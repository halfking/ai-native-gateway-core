-- Migration 698: pin promote month grouping to Asia/Shanghai
--
-- Each promote_*_hot_to_partition body groups its batch by
-- date_trunc('month', <timestamptz>) BEFORE delegating to the matching
-- ensure_*_partition function. 694 pinned the ensure_* bounds to
-- Asia/Shanghai, but the grouping itself still reads the caller's session
-- TimeZone: in a UTC session, rows in the [00:00, 08:00) +08 window of a
-- month start fall into the PREVIOUS month group, the ensure_* call then
-- creates a partition whose 694-pinned Shanghai bounds do not contain the
-- row, and the INSERT dies with 23514 — stranding the whole batch in the
-- hot table (and, for TTL-trimmed tables like candidate_failure_logs,
-- opening a data-loss window). Production clusters default to
-- Asia/Shanghai so the defect is latent; any UTC session (new host,
-- maintenance psql, bare pgx) triggers it.
--
-- Fix: `SET LOCAL TIME ZONE 'Asia/Shanghai'` as the first statement of
-- each promote body (694 pattern), so grouping always matches the ensure_*
-- bounds convention regardless of the caller session. The promote_request_logs
-- body is the 697 body (695 self-heal demote + system_fingerprint columns)
-- plus the pin; the other eight are the sql/objects/functions canonical
-- bodies plus the pin.
--
-- Idempotent: CREATE OR REPLACE FUNCTION only. No down migration
-- (695/696/697 precedent); pre-698 bodies remain recoverable from git
-- history and from the same objects/ files at the parent commit.

BEGIN;

--
-- Name: promote_candidate_failure_logs_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_candidate_failure_logs_hot_to_partition(
    p_retention interval DEFAULT '8 hours',
    p_batch_size integer DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql
AS $function$
DECLARE
    moved bigint := 0;
    month_rec record;
BEGIN
    -- 698: group months under Asia/Shanghai so boundary rows land in the
    -- same month group the 694-pinned ensure_* target was created for.
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN
        RAISE EXCEPTION 'p_retention must be positive';
    END IF;
    IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN
        RAISE EXCEPTION 'p_batch_size must be between 1 and 50000';
    END IF;

    FOR month_rec IN
        SELECT DISTINCT date_trunc('month', ts) AS month_start
        FROM public.candidate_failure_logs_hot
        WHERE ts < statement_timestamp() - p_retention
        ORDER BY 1
        LIMIT 12
    LOOP
        PERFORM public.ensure_candidate_failure_logs_partition(month_rec.month_start);
    END LOOP;

    CREATE TEMP TABLE _candidate_failure_logs_promotion_batch ON COMMIT DROP AS
    SELECT ctid AS source_ctid, id, request_id, ts, tenant_id, credential_id,
           provider_id, raw_model_name, attempt_index, error_kind, error_message,
           upstream_status_code, upstream_response_body, upstream_response_preview,
           latency_ms, retryable, per_attempt_latency_ms,
           extracted_upstream_status_code, diagnosed_error_kind, context, session_id,
           aggregation_id
    FROM public.candidate_failure_logs_hot
    WHERE ts < statement_timestamp() - p_retention
    ORDER BY ts, ctid
    LIMIT p_batch_size
    FOR UPDATE SKIP LOCKED;

    IF NOT EXISTS (SELECT 1 FROM _candidate_failure_logs_promotion_batch) THEN
        RETURN 0;
    END IF;

    WITH moved_rows AS (
        DELETE FROM public.candidate_failure_logs_hot h
        USING _candidate_failure_logs_promotion_batch b
        WHERE h.ctid = b.source_ctid
        RETURNING h.id, h.request_id, h.ts, h.tenant_id, h.credential_id,
                  h.provider_id, h.raw_model_name, h.attempt_index,
                  h.error_kind, h.error_message, h.upstream_status_code,
                  h.upstream_response_body, h.upstream_response_preview,
                  h.latency_ms, h.retryable, h.per_attempt_latency_ms,
                  h.extracted_upstream_status_code, h.diagnosed_error_kind,
                  h.context, h.session_id, h.aggregation_id
    ), inserted_rows AS (
        INSERT INTO public.candidate_failure_logs (
            id, request_id, ts, tenant_id, credential_id, provider_id,
            raw_model_name, attempt_index, error_kind, error_message,
            upstream_status_code, upstream_response_body, upstream_response_preview,
            latency_ms, retryable, per_attempt_latency_ms,
            extracted_upstream_status_code, diagnosed_error_kind, context, session_id,
            aggregation_id
        )
        SELECT id, request_id, ts, tenant_id, credential_id, provider_id,
               raw_model_name, attempt_index, error_kind, error_message,
               upstream_status_code, upstream_response_body, upstream_response_preview,
               latency_ms, retryable, per_attempt_latency_ms,
               extracted_upstream_status_code, diagnosed_error_kind, context, session_id,
               aggregation_id
        FROM moved_rows
        RETURNING 1
    )
    SELECT count(*) INTO moved FROM inserted_rows;

    RETURN moved;
END;
$function$;

--
-- Name: promote_credential_model_index_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_credential_model_index_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  -- 698: group months under Asia/Shanghai so boundary rows land in the
  -- same month group the 694-pinned ensure_* target was created for.
  SET LOCAL TIME ZONE 'Asia/Shanghai';
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  -- Rows route by bucket, so pre-ensure the months the moved rows will land in.
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', bucket) AS month_start
    FROM public.credential_model_index_hot
    WHERE updated_at < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_credential_model_index_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT bucket, credential_id, raw_model FROM public.credential_model_index_hot
    WHERE updated_at < now() - p_retention
    ORDER BY updated_at, bucket, credential_id, raw_model LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.credential_model_index_hot h USING batch b
    WHERE h.bucket = b.bucket AND h.credential_id = b.credential_id AND h.raw_model = b.raw_model
    RETURNING h.bucket, h.credential_id, h.raw_model, h.canonical_id, h.billing_mode,
      h.unit_price_in_per_1m, h.unit_price_out_per_1m, h.context_window,
      h.success_rate, h.p95_latency_ms, h.active_sessions, h.concurrency_limit,
      h.pressure_ratio, h.score_smart, h.score_speed_first, h.score_cost_first, h.updated_at
  ), inserted AS (
    INSERT INTO public.credential_model_index (
      bucket, credential_id, raw_model, canonical_id, billing_mode,
      unit_price_in_per_1m, unit_price_out_per_1m, context_window,
      success_rate, p95_latency_ms, active_sessions, concurrency_limit,
      pressure_ratio, score_smart, score_speed_first, score_cost_first, updated_at)
    SELECT bucket, credential_id, raw_model, canonical_id, billing_mode,
      unit_price_in_per_1m, unit_price_out_per_1m, context_window,
      success_rate, p95_latency_ms, active_sessions, concurrency_limit,
      pressure_ratio, score_smart, score_speed_first, score_cost_first, updated_at
    FROM moved_rows
    RETURNING credential_id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

--
-- Name: promote_credit_ledger_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_credit_ledger_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  -- 698: group months under Asia/Shanghai so boundary rows land in the
  -- same month group the 694-pinned ensure_* target was created for.
  SET LOCAL TIME ZONE 'Asia/Shanghai';
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', created_at) AS month_start
    FROM public.credit_ledger_hot
    WHERE created_at < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_credit_ledger_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT id FROM public.credit_ledger_hot
    WHERE created_at < now() - p_retention
    ORDER BY created_at, id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.credit_ledger_hot h USING batch b
    WHERE h.id = b.id
    RETURNING h.id, h.tenant_id, h.entry_type, h.amount, h.balance_after,
      h.ref_type, h.ref_id, h.note, h.created_at, h.pool
  ), inserted AS (
    INSERT INTO public.credit_ledger (
      id, tenant_id, entry_type, amount, balance_after,
      ref_type, ref_id, note, created_at, pool)
    SELECT id, tenant_id, entry_type, amount, balance_after,
      ref_type, ref_id, note, created_at, pool
    FROM moved_rows
    RETURNING id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

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

--
-- Name: promote_request_logs_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_request_logs_hot_to_partition(p_retention interval DEFAULT '8 hours'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    moved bigint := 0;
    month_rec record;
    part_rec record;
    v_demoted bigint := 0;
    v_part_demoted bigint := 0;
BEGIN
    -- 698: group months under Asia/Shanghai so boundary rows land in the
    -- same month group the 694-pinned ensure_* target was created for.
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN
        RAISE EXCEPTION 'p_retention must be positive';
    END IF;
    IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN
        RAISE EXCEPTION 'p_batch_size must be between 1 and 50000';
    END IF;

    -- Guarantee a routing target exists for every affected month before any
    -- row leaves the hot table (ensure_request_logs_partition is idempotent).
    FOR month_rec IN
        SELECT DISTINCT date_trunc('month', ts) AS month_start
        FROM public.request_logs_hot
        WHERE ts < statement_timestamp() - p_retention
        ORDER BY 1
        LIMIT 12
    LOOP
        PERFORM public.ensure_request_logs_partition(month_rec.month_start);
    END LOOP;

    -- 2026-09-12 (P2 self-heal): demote hot final-success claims that a
    -- promote batch would otherwise reject. The claim guard
    -- (claimSessionFinalSuccess promoted-partition NOT EXISTS) is the
    -- first line of defense; this is the promote-side backstop so a
    -- poisoned row can never jam the single-CTE atomic batch again
    -- (first-come claim wins: the promoted earlier row keeps TRUE, the
    -- hot newcomer degrades to a superseded FALSE row, history preserved).
    FOR part_rec IN
        SELECT c.relname AS partition_name
          FROM pg_inherits i
          JOIN pg_class p ON p.oid = i.inhparent
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_am am ON am.oid = c.relam
         WHERE p.relname = 'request_logs'
           AND am.amname = 'heap'
         ORDER BY 1
    LOOP
        EXECUTE format(
            'UPDATE public.request_logs_hot h
                SET is_final_success = FALSE
              WHERE h.is_final_success
                AND h.ts < statement_timestamp() - $1
                AND COALESCE(h.gw_session_id, '''') <> ''''
                AND EXISTS (
                    SELECT 1
                      FROM ONLY %I x
                     WHERE x.gw_session_id = h.gw_session_id
                       AND x.is_final_success
                )',
            part_rec.partition_name)
        USING p_retention;
        GET DIAGNOSTICS v_part_demoted = ROW_COUNT;
        v_demoted := v_demoted + v_part_demoted;
    END LOOP;
    IF v_demoted > 0 THEN
        RAISE WARNING
            'promote_request_logs_hot_to_partition: demoted % hot final-success claim(s) superseded by the promoted winner (migration 695 self-heal)',
            v_demoted;
    END IF;

    -- 2026-08-25 incident fix: the previous implementation DELETEd the batch
    -- from request_logs_hot BEFORE a separately-protected
    -- INSERT INTO request_logs SELECT * FROM _promote_hot_batch. Schema drift
    -- between the two tables (hot carries caller_id / session_correlation_id /
    -- status_code, the partitioned parent carries 14 legacy columns) made the
    -- positional SELECT * fail on every batch, and because the DELETE had
    -- already executed outside the exception sub-block, each failed batch was
    -- silently dropped (the RAISE WARNING even claimed "rows preserved in hot
    -- table"). Rewrite as ONE data-modifying CTE statement: delete and insert
    -- commit atomically and any error propagates to the caller instead of
    -- being swallowed. Columns are explicit so future drift cannot lose
    -- positional alignment; the five hot-side columns whose types diverged
    -- from the parent (protocol_conversion, ir_extensions,
    -- sanitizer_mutations, content_safety_score, dlp_violations) are
    -- intentionally omitted — no code path ever writes them, so promoting
    -- NULLs is pointless and the cross-type casts would be loss-prone.
    WITH batch AS (
        SELECT id, ts
        FROM public.request_logs_hot
        WHERE ts < statement_timestamp() - p_retention
        ORDER BY ts, id
        LIMIT p_batch_size
        FOR UPDATE SKIP LOCKED
    ),
    moved_rows AS (
        DELETE FROM public.request_logs_hot
        WHERE id IN (SELECT id FROM batch)
        RETURNING
                id, request_id, ts, tenant_id, application_id, api_key_id,
                end_user_id, client_model, outbound_model, credential_id, provider_id, canonical_id,
                client_profile, request_mode, prompt_tokens, completion_tokens, total_tokens, cost_usd,
                latency_ms, success, error_kind, search_text, cache_read_tokens, cache_write_tokens,
                identity_hash, virtual_client_id, virtual_ip, virtual_mac, affinity_hit, stream_first_chunk_ms,
                stream_chunk_count, stream_interrupted, stream_done_sent, request_checksum, response_checksum, transform_rule_id,
                egress_protocol, failure_stage, failure_detail_code, request_preview, transform_summary, response_preview,
                stream_done_received, cost_display, cost_currency, usage_source, gw_session_id, gw_task_id,
                request_status, api_key_prefix, owner_user, application_code, key_alias, api_key_owner_user,
                is_auto_request, task_type, auto_profile, auto_decision, auto_confidence, work_type,
                task_type_chosen, confidence_num, model_chosen, strategy_used, credits_charged, parent_request_id,
                compression_reason, compression_strategy, compression_meta, outbound_msg_count, outbound_token_est, outbound_msg_hashes,
                quality_flags, quality_fix_actions, quality_score, upstream_finish_reason, tool_calls, client_endpoint,
                client_timeout, stream_chunk_errors, stream_chunks_sent, client_request_id, upstream_status_code, test_col,
                test_tab_indent, provider_model, attachments, has_attachments, attachment_count, client_ip,
                compression_start_index, compression_end_index, client_forwarded_for, agent_name, agent_type, api_key_fingerprint,
                customer_id, upstream_endpoint, session_title, session_summary, task_id, task_title,
                compression_ratio, cache_hit, cache_tokens_saved, sensitive_keywords, vendor_metadata, client_protocol,
                rate_limit_status, upstream_protocol, reasoning_tokens, image_tokens, audio_tokens, video_tokens,
                provider_tokens, origin_stage, origin_actor, routing_attempts, routing_summary, trace_events,
                canonical_model, t0_arrived_at, t1_total_enqueued_at, t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at,
                t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at, t8_response_start_at, t9_response_end_at, request_type,
                is_final_success, discard_events, token_band,
                system_fingerprint
    )
    INSERT INTO public.request_logs (
                id, request_id, ts, tenant_id, application_id, api_key_id,
                end_user_id, client_model, outbound_model, credential_id, provider_id, canonical_id,
                client_profile, request_mode, prompt_tokens, completion_tokens, total_tokens, cost_usd,
                latency_ms, success, error_kind, search_text, cache_read_tokens, cache_write_tokens,
                identity_hash, virtual_client_id, virtual_ip, virtual_mac, affinity_hit, stream_first_chunk_ms,
                stream_chunk_count, stream_interrupted, stream_done_sent, request_checksum, response_checksum, transform_rule_id,
                egress_protocol, failure_stage, failure_detail_code, request_preview, transform_summary, response_preview,
                stream_done_received, cost_display, cost_currency, usage_source, gw_session_id, gw_task_id,
                request_status, api_key_prefix, owner_user, application_code, key_alias, api_key_owner_user,
                is_auto_request, task_type, auto_profile, auto_decision, auto_confidence, work_type,
                task_type_chosen, confidence_num, model_chosen, strategy_used, credits_charged, parent_request_id,
                compression_reason, compression_strategy, compression_meta, outbound_msg_count, outbound_token_est, outbound_msg_hashes,
                quality_flags, quality_fix_actions, quality_score, upstream_finish_reason, tool_calls, client_endpoint,
                client_timeout, stream_chunk_errors, stream_chunks_sent, client_request_id, upstream_status_code, test_col,
                test_tab_indent, provider_model, attachments, has_attachments, attachment_count, client_ip,
                compression_start_index, compression_end_index, client_forwarded_for, agent_name, agent_type, api_key_fingerprint,
                customer_id, upstream_endpoint, session_title, session_summary, task_id, task_title,
                compression_ratio, cache_hit, cache_tokens_saved, sensitive_keywords, vendor_metadata, client_protocol,
                rate_limit_status, upstream_protocol, reasoning_tokens, image_tokens, audio_tokens, video_tokens,
                provider_tokens, origin_stage, origin_actor, routing_attempts, routing_summary, trace_events,
                canonical_model, t0_arrived_at, t1_total_enqueued_at, t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at,
                t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at, t8_response_start_at, t9_response_end_at, request_type,
                is_final_success, discard_events, token_band,
                system_fingerprint
    )
    SELECT
                id, request_id, ts, tenant_id, application_id, api_key_id,
                end_user_id, client_model, outbound_model, credential_id, provider_id, canonical_id,
                client_profile, request_mode, prompt_tokens, completion_tokens, total_tokens, cost_usd,
                latency_ms, success, error_kind, search_text, cache_read_tokens, cache_write_tokens,
                identity_hash, virtual_client_id, virtual_ip, virtual_mac, affinity_hit, stream_first_chunk_ms,
                stream_chunk_count, stream_interrupted, stream_done_sent, request_checksum, response_checksum, transform_rule_id,
                egress_protocol, failure_stage, failure_detail_code, request_preview, transform_summary, response_preview,
                stream_done_received, cost_display, cost_currency, usage_source, gw_session_id, gw_task_id,
                request_status, api_key_prefix, owner_user, application_code, key_alias, api_key_owner_user,
                is_auto_request, task_type, auto_profile, auto_decision, auto_confidence, work_type,
                task_type_chosen, confidence_num, model_chosen, strategy_used, credits_charged, parent_request_id,
                compression_reason, compression_strategy, compression_meta, outbound_msg_count, outbound_token_est, outbound_msg_hashes,
                quality_flags, quality_fix_actions, quality_score, upstream_finish_reason, tool_calls, client_endpoint,
                client_timeout, stream_chunk_errors, stream_chunks_sent, client_request_id, upstream_status_code, test_col,
                test_tab_indent, provider_model, attachments, has_attachments, attachment_count, client_ip,
                compression_start_index, compression_end_index, client_forwarded_for, agent_name, agent_type, api_key_fingerprint,
                customer_id, upstream_endpoint, session_title, session_summary, task_id, task_title,
                compression_ratio, cache_hit, cache_tokens_saved, sensitive_keywords, vendor_metadata, client_protocol,
                rate_limit_status, upstream_protocol, reasoning_tokens, image_tokens, audio_tokens, video_tokens,
                provider_tokens, origin_stage, origin_actor, routing_attempts, routing_summary, trace_events,
                canonical_model, t0_arrived_at, t1_total_enqueued_at, t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at,
                t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at, t8_response_start_at, t9_response_end_at, request_type,
                is_final_success, discard_events, token_band,
                system_fingerprint
    FROM moved_rows;

    GET DIAGNOSTICS moved = ROW_COUNT;
    RETURN moved;
END;
$$;

--
-- Name: promote_request_wal_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_request_wal_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  -- 698: group months under Asia/Shanghai so boundary rows land in the
  -- same month group the 694-pinned ensure_* target was created for.
  SET LOCAL TIME ZONE 'Asia/Shanghai';
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', created_at) AS month_start
    FROM public.request_wal_hot
    WHERE created_at < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_request_wal_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT request_id, created_at FROM public.request_wal_hot
    WHERE created_at < now() - p_retention
    ORDER BY created_at, request_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.request_wal_hot h USING batch b
    WHERE h.request_id = b.request_id AND h.created_at = b.created_at
    RETURNING h.request_id, h.tenant_id, h.gw_session_id, h.status, h.stage,
      h.client_model, h.upstream_provider_id, h.upstream_credential_id,
      h.completion_tokens, h.prompt_tokens, h.created_at, h.completed_at,
      h.upstream_request_at, h.upstream_response_at, h.error,
      h.compression_strategy, h.compression_meta
  ), inserted AS (
    INSERT INTO public.request_wal (
      request_id, tenant_id, gw_session_id, status, stage,
      client_model, upstream_provider_id, upstream_credential_id,
      completion_tokens, prompt_tokens, created_at, completed_at,
      upstream_request_at, upstream_response_at, error,
      compression_strategy, compression_meta)
    SELECT request_id, tenant_id, gw_session_id, status, stage,
      client_model, upstream_provider_id, upstream_credential_id,
      completion_tokens, prompt_tokens, created_at, completed_at,
      upstream_request_at, upstream_response_at, error,
      compression_strategy, compression_meta
    FROM moved_rows
    RETURNING request_id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

--
-- Name: promote_routing_decision_log_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_routing_decision_log_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  -- 698: group months under Asia/Shanghai so boundary rows land in the
  -- same month group the 694-pinned ensure_* target was created for.
  SET LOCAL TIME ZONE 'Asia/Shanghai';
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', ts) AS month_start
    FROM public.routing_decision_log_hot
    WHERE ts < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_routing_decision_log_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT request_id, ts FROM public.routing_decision_log_hot
    WHERE ts < now() - p_retention
    ORDER BY ts, request_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.routing_decision_log_hot h USING batch b
    WHERE h.request_id = b.request_id AND h.ts = b.ts
    RETURNING h.ts, h.request_id, h.idempotency_key, h.tenant_id, h.api_key_id,
      h.model, h.chosen_credential_id, h.chosen_provider_id, h.tier,
      h.candidates_tried, h.latency_ms, h.success, h.error_class,
      h.prompt_tokens, h.completion_tokens, h.cost_usd, h.request_bytes,
      h.response_bytes, h.client_model, h.resolved_raw_model, h.sticky_hit,
      h.client_profile, h.outbound_model, h.request_mode, h.identity_hash,
      h.transform_rule_id, h.egress_protocol, h.failure_stage,
      h.failure_detail_code, h.virtual_client_id, h.virtual_ip, h.virtual_mac,
      h.resolution_path, h.canonical_model, h.resolution_raw_models, h.decision_trace
  ), inserted AS (
    INSERT INTO public.routing_decision_log (
      ts, request_id, idempotency_key, tenant_id, api_key_id,
      model, chosen_credential_id, chosen_provider_id, tier,
      candidates_tried, latency_ms, success, error_class,
      prompt_tokens, completion_tokens, cost_usd, request_bytes,
      response_bytes, client_model, resolved_raw_model, sticky_hit,
      client_profile, outbound_model, request_mode, identity_hash,
      transform_rule_id, egress_protocol, failure_stage,
      failure_detail_code, virtual_client_id, virtual_ip, virtual_mac,
      resolution_path, canonical_model, resolution_raw_models, decision_trace)
    SELECT ts, request_id, idempotency_key, tenant_id, api_key_id,
      model, chosen_credential_id, chosen_provider_id, tier,
      candidates_tried, latency_ms, success, error_class,
      prompt_tokens, completion_tokens, cost_usd, request_bytes,
      response_bytes, client_model, resolved_raw_model, sticky_hit,
      client_profile, outbound_model, request_mode, identity_hash,
      transform_rule_id, egress_protocol, failure_stage,
      failure_detail_code, virtual_client_id, virtual_ip, virtual_mac,
      resolution_path, canonical_model, resolution_raw_models, decision_trace
    FROM moved_rows
    RETURNING request_id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

--
-- Name: promote_tool_usage_stats_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_tool_usage_stats_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  -- 698: group months under Asia/Shanghai so boundary rows land in the
  -- same month group the 694-pinned ensure_* target was created for.
  SET LOCAL TIME ZONE 'Asia/Shanghai';
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  -- Rows route by created_at, so pre-ensure those months; the retention
  -- predicate itself stays on usage_date exactly as in migration 348.
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', created_at) AS month_start
    FROM public.tool_usage_stats_hot
    WHERE usage_date < CURRENT_DATE - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_tool_usage_stats_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT tool_id, tenant_id, usage_date FROM public.tool_usage_stats_hot
    WHERE usage_date < CURRENT_DATE - p_retention
    ORDER BY usage_date, tool_id, tenant_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.tool_usage_stats_hot h USING batch b
    WHERE h.tool_id = b.tool_id AND h.tenant_id = b.tenant_id AND h.usage_date = b.usage_date
    RETURNING h.id, h.tool_id, h.tenant_id, h.usage_date, h.call_count,
      h.success_count, h.error_count, h.avg_latency_ms, h.last_called_at,
      h.created_at, h.updated_at
  ), inserted AS (
    INSERT INTO public.tool_usage_stats (
      id, tool_id, tenant_id, usage_date, call_count,
      success_count, error_count, avg_latency_ms, last_called_at,
      created_at, updated_at)
    SELECT id, tool_id, tenant_id, usage_date, call_count,
      success_count, error_count, avg_latency_ms, last_called_at,
      created_at, updated_at
    FROM moved_rows
    RETURNING id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

--
-- Name: promote_usage_ledger_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_usage_ledger_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  -- 698: group months under Asia/Shanghai so boundary rows land in the
  -- same month group the 694-pinned ensure_* target was created for.
  SET LOCAL TIME ZONE 'Asia/Shanghai';
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  -- The month pre-ensure loop must use the same predicate as the batch CTE.
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', ts) AS month_start
    FROM public.usage_ledger_hot
    WHERE ts < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_usage_ledger_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT request_id, ts FROM public.usage_ledger_hot
    WHERE ts < now() - p_retention
    ORDER BY ts, request_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.usage_ledger_hot h USING batch b
    WHERE h.request_id = b.request_id AND h.ts = b.ts
    RETURNING h.request_id, h.ts, h.tenant_id, h.application_id, h.api_key_id,
      h.end_user_id, h.credential_id, h.provider_id, h.canonical_id, h.raw_model_name,
      h.prompt_tokens, h.completion_tokens, h.cache_read_tokens, h.cache_write_tokens,
      h.total_tokens, h.cost_usd, h.latency_ms, h.success, h.error_kind
  ), inserted AS (
    INSERT INTO public.usage_ledger (
      request_id, ts, tenant_id, application_id, api_key_id,
      end_user_id, credential_id, provider_id, canonical_id, raw_model_name,
      prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
      total_tokens, cost_usd, latency_ms, success, error_kind)
    SELECT request_id, ts, tenant_id, application_id, api_key_id,
      end_user_id, credential_id, provider_id, canonical_id, raw_model_name,
      prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
      total_tokens, cost_usd, latency_ms, success, error_kind
    FROM moved_rows
    RETURNING request_id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

INSERT INTO public.schema_migrations (version, description)
VALUES ('698', 'Pin promote hot-to-partition month grouping to Asia/Shanghai')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
