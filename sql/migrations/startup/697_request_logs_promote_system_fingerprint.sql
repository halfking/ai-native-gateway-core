-- Migration 697: promote 函数列清单补 system_fingerprint——指纹写路径贯通
--
-- Motivation (2026-09-12, 挂账闭环):
--   X-System-Fingerprint 响应头由 streaming integrity 捕获后只写入 detector 的
--   context JSONB,migration 603 给 request_logs_hot/parent 建的专用列自建成起
--   全表零行(写路径从未接线),7 天指纹漂移检测器(integrity_fingerprint_drift)
--   自 2026-07-28 起在空集上空转。本次同批变更把捕获的指纹落到
--   request_logs_hot.system_fingerprint(telemetry 事务内独立小 UPDATE,仿
--   upsertProtocolMetadata 先例);promote 的显式列清单(602/688/695 三列表)
--   此前不含该列,晋升行会静默丢指纹(父表默认 NULL),8h 保留窗口之外漂移
--   检测随即失明——本迁移把 system_fingerprint 追加进 RETURNING/INSERT/SELECT
--   三个清单尾部,位置对齐由 migration_697_test.go 的
--   TestMigration697ColumnListsAligned 钉住。
--
-- Fix: 函数体整段承 695(含 final-success 冲突自愈 demote 与 DEFAULT '8 hours'),
--   仅三列清单尾部追加 system_fingerprint。列在 hot(603)与 parent(487)均已存在。
--
-- Impact: 每批多搬运一列 TEXT(绝大多数行 NULL,TOAST 开销可忽略)。语义零变化。
--
-- Idempotent: YES (纯 CREATE OR REPLACE)。
-- Down: 无。紧急回滚 = 重放 695 中该函数的定义。
-- 编号: 697 在生产双账本(schema_migrations + gateway_db_revision_sequences)
--   均未被本库或其他项目占用(2026-09-12 查证)。

\set ON_ERROR_STOP on

BEGIN;

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

COMMIT;
