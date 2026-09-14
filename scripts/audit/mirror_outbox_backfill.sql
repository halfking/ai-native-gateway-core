-- mirror_outbox_backfill.sql — GAP-2 历史回填（存储优化 v2 §4-S4 前置项②）
--
-- 把近 N 天（默认 7 天，覆盖 GLOBAL_G2 的 24h 滚动窗口有余量）v1 终态行中
-- 缺 session_turns 的行投影为 telemetry.RequestLogEntry 同形态 JSON，灌入
-- session_mirror_outbox（source='backfill'），由网关内 reaper
-- （sessions_v2.mirror_outbox_replay，默认开）无差别消化重放。
--
-- 契约：payload 键名对齐 RequestLogEntry 的 json tag（v1 列与 tag 几乎同名，
-- 例外：ts → event_at、is_final_success → success）。重放走
-- entryToProcessedRequest 同一桥接 + v2.Write（request_id 幂等）。
--
-- 投影仅用 request_logs_hot 与 request_logs 父表的共有列（两表不同构：
-- hot 独有 status_code/caller_id/session_correlation_id，父表独有
-- is_terminal/outbound_body/request_depth——父表的 outbound_body 只在
-- S1b cutover 过渡窗有值，仍在 bodies CTE 中经 bodies_hot 取近 7 天值）。
-- project_id / namespace / submit_mode_header 为 mirror-only 字段，v1 无源，
-- 回填行保持零值（entryToProcessedRequest 的 detector 走推断路径）。
--
-- 用法（父表∪hot 双侧语义与 scripts/audit/storage_observation_round.sh 一致）：
--   psql "$LLM_GATEWAY_DSN" -f scripts/audit/mirror_outbox_backfill.sql
-- 幂等：ON CONFLICT (request_id) DO NOTHING，可重复执行。
-- 回看窗口：编辑下方 \set days。

\set days 7

WITH bodies AS (
    SELECT DISTINCT ON (request_id)
           request_id, request_body, response_body, outbound_body
    FROM (
        SELECT request_id, request_body, response_body, outbound_body
        FROM public.request_logs_bodies_hot
        UNION ALL
        SELECT request_id, request_body, response_body, outbound_body
        FROM public.request_logs_bodies
    ) u
),
v1 AS (
    SELECT request_id, ts, tenant_id, gw_session_id, is_final_success,
           latency_ms, prompt_tokens, completion_tokens,
           cache_read_tokens, cache_write_tokens, reasoning_tokens,
           image_tokens, audio_tokens, video_tokens, provider_tokens,
           cost_usd, cost_display, cost_currency, credits_charged,
           usage_source, error_kind, request_status,
           upstream_status_code, upstream_finish_reason,
           client_model, outbound_model, canonical_model, canonical_id,
           credential_id, provider_id, application_id, api_key_id,
           api_key_owner_user, end_user_id, customer_id, client_ip,
           client_forwarded_for, agent_name, agent_type, client_protocol,
           upstream_protocol, protocol_conversion, client_request_id,
           client_timeout, client_endpoint, egress_protocol,
           task_type, is_auto_request, auto_decision, auto_confidence,
           auto_profile, work_type, task_type_chosen, confidence_num,
           model_chosen, routing_attempts, routing_summary,
           parent_request_id, compression_strategy, compression_reason,
           compression_meta, token_band, request_mode, client_profile,
           affinity_hit, request_class, due_at, system_fingerprint,
           identity_hash, response_checksum, request_preview,
           transform_summary, response_preview, failure_stage,
           failure_detail_code, stream_first_chunk_ms, stream_chunk_count,
           stream_done_received, stream_interrupted, origin_stage,
           origin_actor, attachments, outbound_msg_count,
           outbound_token_est, outbound_msg_hashes, quality_flags,
           quality_fix_actions, quality_score
    FROM public.request_logs_hot
    UNION ALL
    SELECT request_id, ts, tenant_id, gw_session_id, is_final_success,
           latency_ms, prompt_tokens, completion_tokens,
           cache_read_tokens, cache_write_tokens, reasoning_tokens,
           image_tokens, audio_tokens, video_tokens, provider_tokens,
           cost_usd, cost_display, cost_currency, credits_charged,
           usage_source, error_kind, request_status,
           upstream_status_code, upstream_finish_reason,
           client_model, outbound_model, canonical_model, canonical_id,
           credential_id, provider_id, application_id, api_key_id,
           api_key_owner_user, end_user_id, customer_id, client_ip,
           client_forwarded_for, agent_name, agent_type, client_protocol,
           upstream_protocol, protocol_conversion, client_request_id,
           client_timeout, client_endpoint, egress_protocol,
           task_type, is_auto_request, auto_decision, auto_confidence,
           auto_profile, work_type, task_type_chosen, confidence_num,
           model_chosen, routing_attempts, routing_summary,
           parent_request_id, compression_strategy, compression_reason,
           compression_meta, token_band, request_mode, client_profile,
           affinity_hit, request_class, due_at, system_fingerprint,
           identity_hash, response_checksum, request_preview,
           transform_summary, response_preview, failure_stage,
           failure_detail_code, stream_first_chunk_ms, stream_chunk_count,
           stream_done_received, stream_interrupted, origin_stage,
           origin_actor, attachments, outbound_msg_count,
           outbound_token_est, outbound_msg_hashes, quality_flags,
           quality_fix_actions, quality_score
    FROM public.request_logs
),
missing AS (
    SELECT v1.*, b.request_body, b.response_body, b.outbound_body
    FROM v1
    LEFT JOIN bodies b ON b.request_id = v1.request_id
    WHERE v1.is_final_success IS TRUE
      AND v1.request_id IS NOT NULL
      AND v1.ts > now() - (:days || ' days')::interval
      AND NOT EXISTS (SELECT 1 FROM public.session_turns_hot t WHERE t.request_id = v1.request_id)
      AND NOT EXISTS (SELECT 1 FROM public.session_turns t      WHERE t.request_id = v1.request_id)
)
INSERT INTO public.session_mirror_outbox
    (tenant_id, request_id, session_id, source, fail_reason, payload)
SELECT
    COALESCE(NULLIF(m.tenant_id, ''), 'default'),
    m.request_id,
    COALESCE(NULLIF(m.gw_session_id, ''), 'backfill:' || m.request_id),
    'backfill',
    'g2_backfill',
    jsonb_build_object(
        'request_id',             m.request_id,
        'event_at',               m.ts,
        'tenant_id',              COALESCE(m.tenant_id, 'default'),
        'gw_session_id',          NULLIF(m.gw_session_id, ''),
        'success',                m.is_final_success,
        'latency_ms',             m.latency_ms,
        'prompt_tokens',          m.prompt_tokens,
        'completion_tokens',      m.completion_tokens,
        'cache_read_tokens',      m.cache_read_tokens,
        'cache_write_tokens',     m.cache_write_tokens,
        'reasoning_tokens',       m.reasoning_tokens,
        'image_tokens',           m.image_tokens,
        'audio_tokens',           m.audio_tokens,
        'video_tokens',           m.video_tokens,
        'provider_tokens',        m.provider_tokens,
        'cost_usd',               m.cost_usd,
        'cost_display',           m.cost_display,
        'cost_currency',          m.cost_currency,
        'credits_charged',        m.credits_charged,
        'usage_source',           m.usage_source,
        'error_kind',             m.error_kind,
        'request_status',         m.request_status,
        'upstream_status_code',   m.upstream_status_code,
        'upstream_finish_reason', m.upstream_finish_reason,
        'client_model',           m.client_model,
        'outbound_model',         m.outbound_model,
        'canonical_model',        m.canonical_model,
        'canonical_id',           m.canonical_id,
        'credential_id',          m.credential_id,
        'provider_id',            m.provider_id,
        'application_id',         m.application_id,
        'api_key_id',             m.api_key_id,
        'api_key_owner_user',     m.api_key_owner_user,
        'end_user_id',            m.end_user_id,
        'customer_id',            m.customer_id,
        'client_ip',              m.client_ip,
        'client_forwarded_for',   m.client_forwarded_for,
        'agent_name',             m.agent_name,
        'agent_type',             m.agent_type,
        'client_protocol',        m.client_protocol,
        'upstream_protocol',      m.upstream_protocol,
        'protocol_conversion',    m.protocol_conversion,
        'client_request_id',      m.client_request_id,
        'client_timeout',         m.client_timeout,
        'client_endpoint',        m.client_endpoint,
        'egress_protocol',        m.egress_protocol,
        'task_type',              m.task_type,
        'is_auto_request',        m.is_auto_request,
        'auto_decision',          m.auto_decision,
        'auto_confidence',        m.auto_confidence,
        'auto_profile',           m.auto_profile,
        'work_type',              m.work_type,
        'task_type_chosen',       m.task_type_chosen,
        'confidence_num',         m.confidence_num,
        'model_chosen',           m.model_chosen,
        'routing_attempts',       m.routing_attempts,
        'routing_summary',        m.routing_summary,
        'parent_request_id',      m.parent_request_id,
        'compression_strategy',   m.compression_strategy,
        'compression_reason',     m.compression_reason,
        'compression_meta',       m.compression_meta,
        'token_band',             m.token_band,
        'request_mode',           m.request_mode,
        'client_profile',         m.client_profile,
        'affinity_hit',           m.affinity_hit,
        'request_class',          m.request_class,
        'due_at',                 m.due_at,
        'system_fingerprint',     m.system_fingerprint,
        'identity_hash',          m.identity_hash,
        'response_checksum',      m.response_checksum,
        'request_preview',        m.request_preview,
        'transform_summary',      m.transform_summary,
        'response_preview',       m.response_preview,
        'failure_stage',          m.failure_stage,
        'failure_detail_code',    m.failure_detail_code,
        'stream_first_chunk_ms',  m.stream_first_chunk_ms,
        'stream_chunk_count',     m.stream_chunk_count,
        'stream_done_received',   m.stream_done_received,
        'stream_interrupted',     m.stream_interrupted,
        'origin_stage',           m.origin_stage,
        'origin_actor',           m.origin_actor,
        'attachments',            m.attachments,
        'outbound_msg_count',     m.outbound_msg_count,
        'outbound_token_est',     m.outbound_token_est,
        'outbound_msg_hashes',    m.outbound_msg_hashes,
        'quality_flags',          m.quality_flags,
        'quality_fix_actions',    m.quality_fix_actions,
        'quality_score',          m.quality_score,
        'request_body',           CASE WHEN m.request_body  IS NULL THEN NULL ELSE m.request_body::text  END,
        'response_body',          CASE WHEN m.response_body IS NULL THEN NULL ELSE m.response_body::text END,
        'outbound_body',          m.outbound_body
    )
FROM missing m
ON CONFLICT (request_id) DO NOTHING;

-- 结果反馈：登记行数与重放前的 pending 深度
SELECT 'backfill_rows' AS k, count(*)::text AS v
FROM public.session_mirror_outbox WHERE source = 'backfill'
UNION ALL
SELECT 'pending_now', count(*)::text
FROM public.session_mirror_outbox WHERE status = 'pending';
