-- Migration 688: promote_*_hot_to_partition 默认保留期对齐 Go 调度器实际值
--
-- Motivation (2026-09-08 24h 审计第二轮 Track C#7, P1 存储治理):
--   bg.PartitionManager (bg/partition_manager.go resolvePromoteConfig) 每次
--   promote tick 都显式传参 ($1::interval, $2::int),SQL DEFAULT 不影响自动
--   调度;但手工/运维直接 `SELECT promote_xxx();` 不传参时会走 SQL DEFAULT。
--   多数函数的 DEFAULT 仍是 '7 days'(甚至 candidate_failure_logs 是
--   '24 hours'),与 Go 调度器实际使用的 8h(lifecycle.hot_retention_hours
--   默认 8;DefaultRetentionWindow = 8h)漂移:手工补跑一次 = 多保留 21 天
--   hot 数据,与"hot 表 8 小时不变式"相悖。
--
-- Fix: 对 10 个 DEFAULT 与 Go 调度默认不一致的 promote 函数
--   CREATE OR REPLACE,函数体整段复制各自最新迁移的线上定义,仅改 DEFAULT:
--   (函数            | 旧 DEFAULT  | 新 DEFAULT | Go 调度默认 | 函数体来源)
--   - promote_request_logs_hot_to_partition            '7 days'  → '8 hours' (602)
--   - promote_usage_ledger_hot_to_partition            '7 days'  → '8 hours' (659)
--   - promote_request_wal_hot_to_partition             '7 days'  → '8 hours' (659)
--   - promote_routing_decision_log_hot_to_partition    '7 days'  → '8 hours' (659)
--   - promote_credential_model_index_hot_to_partition  '7 days'  → '8 hours' (659)
--   - promote_tool_usage_stats_hot_to_partition        '7 days'  → '8 hours' (659)
--   - promote_credit_ledger_hot_to_partition           '7 days'  → '8 hours' (659)
--   - promote_request_logs_bodies_hot_to_partition     '7 days'  → '24 hours'(659;
--       Go 侧 lifecycle.request_logs_bodies_retention_hours 默认 24)
--   - promote_candidate_failure_logs_hot_to_partition  '24 hours' → '8 hours' (628)
--   - promote_session_turns_hot_to_partition           '7 days'  → '8 hours' (640)
--   其余 6 个函数(supplier_errors V371 / handoff_logs 534 /
--   session_module_executions 580 / dashboard_access_events 607 /
--   session_bodies 638 / auto_route_selections 658)DEFAULT 已是 '8 hours',
--   与 Go 一致,不动。model_probe_runs(386)已退出 promote 流程
--   (bg promoteSpecs 注释掉,hot 表按 TTL 直接 DELETE),不动。
--
-- Impact: 仅改函数签名的 DEFAULT 常量,函数体零改动;对显式传参的
--   Go 调度器 / admin 手动迁移路径零行为变化。影响面 = 裸调用的默认窗口。
--
-- Idempotent: YES(纯 CREATE OR REPLACE,可重复执行)。
-- Verified: 临时夹具库 fixture_audit688 上 pg_get_functiondef 新 DEFAULT /
--   空表调用返回 0 / 连跑两遍幂等。

-- ============================================================
-- 1) promote_request_logs_hot_to_partition — '7 days' → '8 hours'
--    函数体整段复制自 602_request_logs_promote_atomic.sql(线上最新)
-- ============================================================
CREATE OR REPLACE FUNCTION public.promote_request_logs_hot_to_partition(p_retention interval DEFAULT '8 hours'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    moved bigint := 0;
    month_rec record;
BEGIN
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
                is_final_success, discard_events, token_band
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
                is_final_success, discard_events, token_band
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
                is_final_success, discard_events, token_band
    FROM moved_rows;

    GET DIAGNOSTICS moved = ROW_COUNT;
    RETURN moved;
END;
$$;

-- ============================================================
-- 2) promote_usage_ledger_hot_to_partition — '7 days' → '8 hours'
--    函数体整段复制自 659_legacy_promote_atomic_cte.sql
-- ============================================================
CREATE OR REPLACE FUNCTION public.promote_usage_ledger_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
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

-- ============================================================
-- 3) promote_request_wal_hot_to_partition — '7 days' → '8 hours'
-- ============================================================
CREATE OR REPLACE FUNCTION public.promote_request_wal_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
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

-- ============================================================
-- 4) promote_routing_decision_log_hot_to_partition — '7 days' → '8 hours'
-- ============================================================
CREATE OR REPLACE FUNCTION public.promote_routing_decision_log_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
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

-- ============================================================
-- 5) promote_credential_model_index_hot_to_partition — '7 days' → '8 hours'
-- ============================================================
CREATE OR REPLACE FUNCTION public.promote_credential_model_index_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
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

-- ============================================================
-- 6) promote_tool_usage_stats_hot_to_partition — '7 days' → '8 hours'
-- ============================================================
CREATE OR REPLACE FUNCTION public.promote_tool_usage_stats_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
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

-- ============================================================
-- 7) promote_credit_ledger_hot_to_partition — '7 days' → '8 hours'
-- ============================================================
CREATE OR REPLACE FUNCTION public.promote_credit_ledger_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
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

-- ============================================================
-- 8) promote_request_logs_bodies_hot_to_partition — '7 days' → '24 hours'
--    (Go 侧 lifecycle.request_logs_bodies_retention_hours 默认 24)
-- ============================================================
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

-- ============================================================
-- 9) promote_candidate_failure_logs_hot_to_partition — '24 hours' → '8 hours'
--    函数体整段复制自 628_candidate_failure_logs_promote_atomic_v3.sql(线上最新,
--    晚于 deploy V367 的 v2 定义)
-- ============================================================
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

-- ============================================================
-- 10) promote_session_turns_hot_to_partition — '7 days' → '8 hours'
--    函数体整段复制自 640_session_turns_protocol_fields.sql(线上最新)
-- ============================================================
CREATE OR REPLACE FUNCTION public.promote_session_turns_hot_to_partition(
    p_retention INTERVAL DEFAULT '8 hours',
    p_batch_size INTEGER DEFAULT 5000
)
RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    v_moved BIGINT := 0;
    v_conflicts BIGINT := 0;
    v_partition_date DATE;
    v_partition REGCLASS;
    v_session RECORD;
    v_request RECORD;
BEGIN
    IF p_retention IS NULL OR p_retention <= INTERVAL '0 seconds' THEN
        RAISE EXCEPTION 'p_retention must be a positive interval';
    END IF;

    IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 100000 THEN
        RAISE EXCEPTION 'p_batch_size must be between 1 and 100000';
    END IF;

    IF to_regclass('public.session_turns_hot') IS NULL
       OR to_regclass('public.session_turns') IS NULL THEN
        RAISE EXCEPTION 'session_turns hot and parent tables must both exist';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_partitioned_table
        WHERE partrelid = 'public.session_turns'::regclass
    ) THEN
        RAISE EXCEPTION 'public.session_turns must remain partitioned';
    END IF;

    IF EXISTS (
        WITH parent_columns AS (
            SELECT attname, atttypid, atttypmod, attnotnull
            FROM pg_attribute
            WHERE attrelid = 'public.session_turns'::regclass
              AND attnum > 0 AND NOT attisdropped
        ), hot_columns AS (
            SELECT attname, atttypid, atttypmod, attnotnull
            FROM pg_attribute
            WHERE attrelid = 'public.session_turns_hot'::regclass
              AND attnum > 0 AND NOT attisdropped
        )
        SELECT 1
        FROM parent_columns p
        FULL JOIN hot_columns h USING (attname)
        WHERE p.attname IS NULL OR h.attname IS NULL
           OR p.atttypid <> h.atttypid
           OR p.atttypmod <> h.atttypmod
           OR p.attnotnull <> h.attnotnull
    ) THEN
        RAISE EXCEPTION 'session_turns hot/parent column contract has drifted';
    END IF;

    PERFORM pg_advisory_xact_lock(
        hashtextextended('public.promote_session_turns_hot_to_partition', 0)
    );

    CREATE TEMP TABLE IF NOT EXISTS session_turns_promotion_batch (
        id BIGINT NOT NULL,
        partition_date DATE NOT NULL,
        tenant_id TEXT NOT NULL,
        session_id TEXT NOT NULL,
        request_id TEXT NOT NULL,
        PRIMARY KEY (id, partition_date)
    ) ON COMMIT DROP;
    TRUNCATE session_turns_promotion_batch;

    SELECT count(*) INTO v_conflicts
    FROM (
        SELECT 1
        FROM public.session_turns_hot h
        WHERE h.ts < statement_timestamp() - p_retention
          AND EXISTS (
              SELECT 1
              FROM public.session_turns archived
              WHERE archived.tenant_id = h.tenant_id
                AND archived.request_id = h.request_id
          )
        LIMIT p_batch_size
    ) conflicts;
    IF v_conflicts > 0 THEN
        RAISE WARNING 'session_turns promote skipped % duplicate hot rows already present in partitions',
            v_conflicts;
    END IF;

    INSERT INTO session_turns_promotion_batch (
        id, partition_date, tenant_id, session_id, request_id
    )
    SELECT h.id, h.partition_date, h.tenant_id, h.session_id, h.request_id
    FROM public.session_turns_hot h
    WHERE h.ts < statement_timestamp() - p_retention
      AND NOT EXISTS (
          SELECT 1
          FROM public.session_turns archived
          WHERE archived.tenant_id = h.tenant_id
            AND archived.request_id = h.request_id
      )
    ORDER BY h.ts, h.id, h.partition_date
    LIMIT p_batch_size;

    FOR v_session IN
        SELECT tenant_id, session_id
        FROM session_turns_promotion_batch
        GROUP BY tenant_id, session_id
        ORDER BY tenant_id, session_id
    LOOP
        PERFORM pg_advisory_xact_lock(
            public.session_turns_advisory_lock_key(
                v_session.tenant_id,
                v_session.session_id
            )
        );
    END LOOP;

    FOR v_request IN
        SELECT tenant_id, request_id
        FROM session_turns_promotion_batch
        GROUP BY tenant_id, request_id
        ORDER BY tenant_id, request_id
    LOOP
        PERFORM pg_advisory_xact_lock(
            public.session_turns_advisory_lock_key(
                v_request.tenant_id,
                'request:' || v_request.request_id
            )
        );
    END LOOP;

    FOR v_partition_date IN
        SELECT DISTINCT partition_date
        FROM session_turns_promotion_batch
    LOOP
        PERFORM public.ensure_sessions_v2_partitions(v_partition_date);
        v_partition := to_regclass(
            format('public.session_turns_%s', to_char(v_partition_date, 'YYYY_MM'))
        );

        IF v_partition IS NULL OR NOT EXISTS (
            SELECT 1
            FROM pg_inherits
            WHERE inhparent = 'public.session_turns'::regclass
              AND inhrelid = v_partition
        ) THEN
            RAISE EXCEPTION 'no attached session_turns partition for partition_date %',
                v_partition_date;
        END IF;
    END LOOP;

    WITH moved AS (
        DELETE FROM public.session_turns_hot h
        USING session_turns_promotion_batch b
        WHERE h.id = b.id
          AND h.partition_date = b.partition_date
        RETURNING
            h.id, h.session_id, h.turn_no, h.tenant_id, h.request_id,
            h.project_id, h.namespace, h.parent_request_id, h.task_type,
            h.ts, h.submit_mode, h.compression_applied,
            h.compression_strategy, h.compression_meta,
            h.compression_tokens_saved, h.injection_verdict,
            h.output_verdict, h.model, h.provider, h.credential_id,
            h.prompt_tokens, h.completion_tokens, h.cache_read_tokens,
            h.cache_write_tokens, h.cost_usd, h.latency_ms, h.status_code,
            h.success, h.error_kind, h.source_kind, h.quality,
            h.partition_date, h.attachment_count, h.attachment_total_bytes,
            h.multimodal_types, h.attempt_no, h.tools, h.title, h.summary, h.digest,
            h.aggregate_applied_at, h.t0_arrived_at,
            h.t1_total_enqueued_at, h.t2_total_dequeued_at,
            h.t3_model_enqueued_at, h.t4_model_dequeued_at,
            h.t5_cred_enqueued_at, h.t6_cred_dequeued_at,
            h.t7_forward_start_at, h.t8_response_start_at,
            h.t9_response_end_at,
            h.client_protocol, h.upstream_protocol, h.ir_metadata
    ), inserted AS (
        INSERT INTO public.session_turns (
            id, session_id, turn_no, tenant_id, request_id,
            project_id, namespace, parent_request_id, task_type,
            ts, submit_mode, compression_applied, compression_strategy,
            compression_meta, compression_tokens_saved, injection_verdict,
            output_verdict, model, provider, credential_id, prompt_tokens,
            completion_tokens, cache_read_tokens, cache_write_tokens,
            cost_usd, latency_ms, status_code, success, error_kind,
            source_kind, quality, partition_date, attachment_count,
            attachment_total_bytes, multimodal_types, attempt_no, tools,
            title, summary, digest, aggregate_applied_at, t0_arrived_at,
            t1_total_enqueued_at, t2_total_dequeued_at,
            t3_model_enqueued_at, t4_model_dequeued_at,
            t5_cred_enqueued_at, t6_cred_dequeued_at,
            t7_forward_start_at, t8_response_start_at, t9_response_end_at,
            client_protocol, upstream_protocol, ir_metadata
        )
        SELECT
            id, session_id, turn_no, tenant_id, request_id,
            project_id, namespace, parent_request_id, task_type,
            ts, submit_mode, compression_applied, compression_strategy,
            compression_meta, compression_tokens_saved, injection_verdict,
            output_verdict, model, provider, credential_id, prompt_tokens,
            completion_tokens, cache_read_tokens, cache_write_tokens,
            cost_usd, latency_ms, status_code, success, error_kind,
            source_kind, quality, partition_date, attachment_count,
            attachment_total_bytes, multimodal_types, attempt_no, tools,
            title, summary, digest, aggregate_applied_at, t0_arrived_at,
            t1_total_enqueued_at, t2_total_dequeued_at,
            t3_model_enqueued_at, t4_model_dequeued_at,
            t5_cred_enqueued_at, t6_cred_dequeued_at,
            t7_forward_start_at, t8_response_start_at, t9_response_end_at,
            client_protocol, upstream_protocol, ir_metadata
        FROM moved
        RETURNING 1
    )
    SELECT count(*) INTO v_moved FROM inserted;

    RETURN v_moved;
END;
$$;
