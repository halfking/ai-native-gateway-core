-- ===========================================================================
-- File:          sql/migrations/startup/734_request_logs_view_details_join.sql
-- Migration:     734
-- Database:      llm_gateway
-- Purpose:       会话存储解耦 v3（docs/storage/2026-09-20-session-storage-
--                decoupling-plan.md §3.2）：710 拼装视图的 session 分支
--                LEFT JOIN session_turn_details(_hot)（733 特征层），把 30 个
--                NULL 占位列替换为真实特征列——管理端/回放/分析读端不再需要
--                回源 request_logs，S3 视图迁移与 S4 停写的数据前提。
--
--                语义保持：LEFT JOIN（details 行缺失时输出 NULL，与 710 的
--                NULL 占位行为逐位一致，双写/回填窗口零行为漂移）。
--                JOIN 键 = (tenant_id, request_id, partition_date)，与
--                session_turns(_hot) 的冲突键对齐。
--
--                仍保留 NULL：id / test_col / test_tab_indent / provider_model
--                （无源或废弃，登记于 710 列映射契约）。
--
-- 依赖：733（session_turn_details 表族）。
-- Down: 734_request_logs_view_details_join.down.sql 重建 710 形态。
-- ===========================================================================

BEGIN;

DO $$
DECLARE
  proj          TEXT;
  names         TEXT;
  v2_ddl        TEXT;
  append_cols   TEXT := 'source.request_class, source.due_at';
  lateral_h     TEXT := 'h.request_class, h.due_at';
  lateral_p     TEXT := 'p.request_class, p.due_at';
  base_has_fp   BOOLEAN;
  base_has_raw  BOOLEAN;
BEGIN
  -- ── 前置：details 表族必须就位（733）──────────────────────────────────
  IF to_regclass('public.session_turn_details') IS NULL
     OR to_regclass('public.session_turn_details_hot') IS NULL THEN
      RAISE EXCEPTION 'migration 734 requires session_turn_details family (run 733 first)';
  END IF;
  IF to_regclass('public.session_turns') IS NULL
     OR to_regclass('public.session_turns_hot') IS NULL THEN
      RAISE EXCEPTION 'migration 734 requires session_turns family (run 430/513/526 first)';
  END IF;

  -- ── 基础交集形态探测（承 696/700 链）────────────────────────────────────
  SELECT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = 'public'
        AND table_name = 'request_logs_with_current_month_without_customer_id'
        AND column_name = 'system_fingerprint'
  ), EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = 'public'
        AND table_name = 'request_logs_with_current_month_without_customer_id'
        AND column_name = 'raw_model_name'
  ) INTO base_has_fp, base_has_raw;
  IF NOT base_has_fp THEN
    append_cols := append_cols || ', source.system_fingerprint';
    lateral_h   := lateral_h || ', h.system_fingerprint';
    lateral_p   := lateral_p || ', p.system_fingerprint';
  END IF;
  IF NOT base_has_raw THEN
    append_cols := append_cols || ', source.raw_model_name';
    lateral_h   := lateral_h || ', h.raw_model_name';
    lateral_p   := lateral_p || ', p.raw_model_name';
  END IF;

  -- ── 会话分支 113 列投影（别名固定 t / 特征层 d；hot/parent 复用）─────────
  --    734 变更点仅 30 个 d.* 替换；其余与 710 逐字一致。
  proj := $proj$
NULL::bigint AS id
        , t.request_id AS request_id
        , t.ts AS ts
        , t.tenant_id::text AS tenant_id
        , (CASE WHEN t.application_id ~ '^[0-9]+$' THEN t.application_id::bigint END) AS application_id
        , (CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END) AS api_key_id
        , t.end_user_id AS end_user_id
        , d.client_model AS client_model
        , t.model AS outbound_model
        , (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END) AS credential_id
        , d.provider_id AS provider_id
        , t.canonical_id AS canonical_id
        , d.client_profile AS client_profile
        , t.submit_mode AS request_mode
        , t.prompt_tokens AS prompt_tokens
        , t.completion_tokens AS completion_tokens
        , NULLIF(COALESCE(t.prompt_tokens, 0) + COALESCE(t.completion_tokens, 0), 0) AS total_tokens
        , t.cost_usd::numeric(14,8) AS cost_usd
        , t.latency_ms AS latency_ms
        , t.success AS success
        , t.error_kind AS error_kind
        , t.search_text AS search_text
        , t.cache_read_tokens AS cache_read_tokens
        , t.cache_write_tokens AS cache_write_tokens
        , t.identity_hash AS identity_hash
        , t.virtual_client_id AS virtual_client_id
        , d.virtual_ip AS virtual_ip
        , d.virtual_mac AS virtual_mac
        , d.affinity_hit AS affinity_hit
        , t.stream_first_chunk_ms AS stream_first_chunk_ms
        , t.stream_chunk_count AS stream_chunk_count
        , t.stream_interrupted AS stream_interrupted
        , t.stream_done_sent AS stream_done_sent
        , t.request_checksum AS request_checksum
        , t.response_checksum AS response_checksum
        , d.transform_rule_id AS transform_rule_id
        , t.egress_protocol AS egress_protocol
        , t.failure_stage AS failure_stage
        , t.failure_detail_code AS failure_detail_code
        , t.request_preview AS request_preview
        , t.transform_summary AS transform_summary
        , t.response_preview AS response_preview
        , t.stream_done_sent AS stream_done_received
        , t.cost_display::numeric(14,8) AS cost_display
        , t.cost_currency AS cost_currency
        , t.usage_source AS usage_source
        , (CASE WHEN t.session_id LIKE 'sys:%' THEN NULL ELSE t.session_id END) AS gw_session_id
        , d.gw_task_id AS gw_task_id
        , (CASE WHEN t.success IS NULL THEN NULL WHEN t.success THEN 'success' WHEN t.status_code = 429 THEN 'rate_limited' ELSE 'failure' END) AS request_status
        , d.api_key_prefix AS api_key_prefix
        , d.owner_user AS owner_user
        , d.application_code AS application_code
        , d.key_alias AS key_alias
        , d.api_key_owner_user AS api_key_owner_user
        , t.is_auto_request AS is_auto_request
        , t.task_type AS task_type
        , d.auto_profile AS auto_profile
        , (CASE WHEN NULLIF(t.auto_decision, '') IS NOT NULL THEN t.auto_decision::jsonb END) AS auto_decision
        , t.auto_confidence::numeric(4,3) AS auto_confidence
        , t.work_type AS work_type
        , t.task_type_chosen AS task_type_chosen
        , d.confidence_num AS confidence_num
        , d.model_chosen AS model_chosen
        , d.strategy_used AS strategy_used
        , t.credits_charged AS credits_charged
        , t.parent_request_id AS parent_request_id
        , d.compression_reason AS compression_reason
        , t.compression_strategy AS compression_strategy
        , t.compression_meta AS compression_meta
        , d.outbound_msg_count AS outbound_msg_count
        , d.outbound_token_est AS outbound_token_est
        , d.outbound_msg_hashes AS outbound_msg_hashes
        , d.quality_flags AS quality_flags
        , d.quality_fix_actions AS quality_fix_actions
        , d.quality_score AS quality_score
        , t.upstream_finish_reason AS upstream_finish_reason
        , t.tools AS tool_calls
        , t.client_endpoint AS client_endpoint
        , t.client_timeout AS client_timeout
        , d.stream_chunk_errors AS stream_chunk_errors
        , d.stream_chunks_sent AS stream_chunks_sent
        , t.client_request_id AS client_request_id
        , t.upstream_status_code AS upstream_status_code
        , NULL::text[] AS test_col
        , NULL::text AS test_tab_indent
        , NULL::text AS provider_model
        , d.attachments AS attachments
        , (CASE WHEN t.attachment_count IS NULL THEN NULL WHEN t.attachment_count > 0 THEN true ELSE false END) AS has_attachments
        , t.attachment_count AS attachment_count
        , t.routing_attempts AS routing_attempts
        , t.routing_summary AS routing_summary
        , t.agent_name AS agent_name
        , t.agent_type AS agent_type
        , t.client_protocol::character varying(50) AS client_protocol
        , t.canonical_model AS canonical_model
        , t.t0_arrived_at AS t0_arrived_at
        , t.t1_total_enqueued_at AS t1_total_enqueued_at
        , t.t2_total_dequeued_at AS t2_total_dequeued_at
        , t.t3_model_enqueued_at AS t3_model_enqueued_at
        , t.t4_model_dequeued_at AS t4_model_dequeued_at
        , t.t5_cred_enqueued_at AS t5_cred_enqueued_at
        , t.t6_cred_dequeued_at AS t6_cred_dequeued_at
        , t.t7_forward_start_at AS t7_forward_start_at
        , t.t8_response_start_at AS t8_response_start_at
        , t.t9_response_end_at AS t9_response_end_at
        , d.request_type AS request_type
        , t.is_final_success AS is_final_success
        , t.origin_actor::character varying(255) AS origin_actor
        , t.customer_id AS customer_id
        , d.request_class AS request_class
        , d.due_at AS due_at
        , t.system_fingerprint AS system_fingerprint
        , t.raw_model_name AS raw_model_name
$proj$;

  -- ── 113 列契约顺序（与 710 完全一致，UNION ALL 按位置匹型）──────────────
  names := $names$id, request_id, ts, tenant_id, application_id, api_key_id, end_user_id, client_model, outbound_model, credential_id, provider_id, canonical_id, client_profile, request_mode, prompt_tokens, completion_tokens, total_tokens, cost_usd, latency_ms, success, error_kind, search_text, cache_read_tokens, cache_write_tokens, identity_hash, virtual_client_id, virtual_ip, virtual_mac, affinity_hit, stream_first_chunk_ms, stream_chunk_count, stream_interrupted, stream_done_sent, request_checksum, response_checksum, transform_rule_id, egress_protocol, failure_stage, failure_detail_code, request_preview, transform_summary, response_preview, stream_done_received, cost_display, cost_currency, usage_source, gw_session_id, gw_task_id, request_status, api_key_prefix, owner_user, application_code, key_alias, api_key_owner_user, is_auto_request, task_type, auto_profile, auto_decision, auto_confidence, work_type, task_type_chosen, confidence_num, model_chosen, strategy_used, credits_charged, parent_request_id, compression_reason, compression_strategy, compression_meta, outbound_msg_count, outbound_token_est, outbound_msg_hashes, quality_flags, quality_fix_actions, quality_score, upstream_finish_reason, tool_calls, client_endpoint, client_timeout, stream_chunk_errors, stream_chunks_sent, client_request_id, upstream_status_code, test_col, test_tab_indent, provider_model, attachments, has_attachments, attachment_count, routing_attempts, routing_summary, agent_name, agent_type, client_protocol, canonical_model, t0_arrived_at, t1_total_enqueued_at, t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at, t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at, t8_response_start_at, t9_response_end_at, request_type, is_final_success, origin_actor, customer_id, request_class, due_at, system_fingerprint, raw_model_name$names$;

  v2_ddl := format($ddl$
    CREATE OR REPLACE VIEW public.request_logs_with_current_month AS
    SELECT %s
    FROM public.session_turns_hot t
    LEFT JOIN public.session_turn_details_hot d
      ON d.tenant_id = t.tenant_id
     AND d.request_id = t.request_id
     AND d.partition_date = t.partition_date
    UNION ALL
    SELECT %s
    FROM public.session_turns t
    LEFT JOIN public.session_turn_details d
      ON d.tenant_id = t.tenant_id
     AND d.request_id = t.request_id
     AND d.partition_date = t.partition_date
    UNION ALL
    SELECT %s
    FROM (
      SELECT v.*, %s
      FROM public.request_logs_with_current_month_without_request_class_due_at v
      LEFT JOIN LATERAL (
          SELECT %s
          FROM public.request_logs_hot h
          WHERE h.request_id = v.request_id AND h.ts = v.ts
          UNION ALL
          SELECT %s
          FROM public.request_logs p
          WHERE p.request_id = v.request_id AND p.ts = v.ts
          LIMIT 1
      ) source ON true
    ) rl
    WHERE NOT EXISTS (SELECT 1 FROM public.session_turns_hot th WHERE th.request_id = rl.request_id)
      AND NOT EXISTS (SELECT 1 FROM public.session_turns tp WHERE tp.request_id = rl.request_id)
  $ddl$, proj, proj, names, append_cols, lateral_h, lateral_p);

  EXECUTE v2_ddl;

  COMMENT ON VIEW public.request_logs_with_current_month IS
    '会话存储解耦 v3（734）: 710 拼装体升级——session 分支 LEFT JOIN '
    'session_turn_details(_hot)（733 特征层，键 tenant_id+request_id+partition_date），'
    '30 个 NULL 占位列换真实特征列（client_model/quality_*/stream_chunk_errors/'
    'request_class 等）。LEFT 语义：details 缺行时 NULL，与 710 逐位兼容。'
    '仍 NULL：id/test_col/test_tab_indent/provider_model。';

END $$;

-- Ledger self-registration（710 惯例）
INSERT INTO public.schema_migrations (version, description)
VALUES ('734', 'session storage v3: canonical view session branches LEFT JOIN session_turn_details(_hot), 30 NULL placeholders replaced by real feature columns')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
