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
-- is_terminal/outbound_body/request_depth；hot.protocol_conversion 为 text
-- 需显式 ::boolean，父表 agent_name/agent_type 为 varchar 显式 ::text）。
-- project_id / namespace / submit_mode_header 为 mirror-only 字段，v1 无源，
-- 回填行保持零值（entryToProcessedRequest 的 detector 走推断路径）。
--
-- 结构注记：RLS 表（request_logs 父表 force RLS、session_turns(_hot) RLS）
-- 在多表组合查询中触发本机 PG 的 "invalid perminfoindex 0 in RTE with
-- relid 0" planner 错误，故每张 RLS 表先单独物化到临时表（单表 SELECT 不
-- 触发），join/反连接全部在临时表间进行。
--
-- 用法（父表∪hot 双侧语义与 scripts/audit/storage_observation_round.sh 一致）：
--   psql "$LLM_GATEWAY_DSN" -f scripts/audit/mirror_outbox_backfill.sql
-- 幂等：ON CONFLICT (request_id) DO NOTHING，可重复执行。
-- 回看窗口：编辑下方 \set days。

\set days 7

-- 全部物化包进单事务：ON COMMIT DROP 依赖事务边界（psql autocommit 会
-- 在每条 CREATE 后立刻提交并删除临时表）。
BEGIN;

\echo '== 1/5 物化 v1 hot 侧 =='
CREATE TEMP TABLE _g2_v1_hot ON COMMIT DROP AS
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
       upstream_protocol, protocol_conversion::boolean AS protocol_conversion,
       client_request_id, client_timeout, client_endpoint, egress_protocol,
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
WHERE is_final_success IS TRUE
  AND request_id IS NOT NULL
  AND ts > now() - (:days || ' days')::interval;

\echo '== 2/5 物化 v1 父表侧 =='
CREATE TEMP TABLE _g2_v1_par ON COMMIT DROP AS
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
       client_forwarded_for, agent_name::text AS agent_name, agent_type::text AS agent_type, client_protocol,
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
WHERE is_final_success IS TRUE
  AND request_id IS NOT NULL
  AND ts > now() - (:days || ' days')::interval;

\echo '== 3/5 物化 v1 bodies（hot∪父表，取最新） =='
CREATE TEMP TABLE _g2_bodies_hot ON COMMIT DROP AS
SELECT request_id, request_body, response_body, outbound_body, ts
FROM public.request_logs_bodies_hot
WHERE ts > now() - (:days + 1 || ' days')::interval;

-- 父表 request_logs_bodies 不物化：当月分区为 columnar 存储，全/窗扫描
-- 分钟级且回填窗口（7 天）与 bodies_hot 热窗（0-7 天）基本重合；已 promote
-- 的临界行缺正文可接受（G1/G2/G3 对账均不涉正文，session_bodies 缺口与
-- 该行 turns 缺失同源）。

CREATE TEMP TABLE _g2_bodies ON COMMIT DROP AS
SELECT DISTINCT ON (request_id)
       request_id, request_body, response_body, outbound_body
FROM _g2_bodies_hot
ORDER BY request_id, ts DESC;

\echo '== 4/5 物化已有 turns 的 request_id（双侧） =='
CREATE TEMP TABLE _g2_has_turn ON COMMIT DROP AS
SELECT DISTINCT request_id FROM public.session_turns_hot WHERE request_id IS NOT NULL
UNION
SELECT DISTINCT request_id FROM public.session_turns WHERE request_id IS NOT NULL;

\echo '== 5/5 合成缺失集并灌入 outbox =='
CREATE TEMP TABLE _g2_missing ON COMMIT DROP AS
SELECT v.request_id, v.tenant_id, v.gw_session_id, b.request_body, b.response_body, b.outbound_body,
       -- v1 全列以 jsonb 形态携带，供 payload 投影直接引用
       to_jsonb(v) AS v1json
FROM (
    SELECT * FROM _g2_v1_hot
    UNION ALL
    SELECT * FROM _g2_v1_par
) v
LEFT JOIN _g2_bodies b ON b.request_id = v.request_id
WHERE NOT EXISTS (SELECT 1 FROM _g2_has_turn t WHERE t.request_id = v.request_id);

INSERT INTO public.session_mirror_outbox
    (tenant_id, request_id, session_id, source, fail_reason, payload)
SELECT
    COALESCE(NULLIF(m.tenant_id, ''), 'default'),
    m.request_id,
    COALESCE(NULLIF(m.gw_session_id, ''), 'backfill:' || m.request_id),
    'backfill',
    'g2_backfill',
    m.v1json
    || jsonb_build_object(
        'event_at',           m.v1json -> 'ts',
        'success',            m.v1json -> 'is_final_success',
        'request_body',       CASE WHEN m.request_body  IS NULL THEN NULL ELSE m.request_body::text  END,
        'response_body',      CASE WHEN m.response_body IS NULL THEN NULL ELSE m.response_body::text END,
        'outbound_body',      m.outbound_body
    )
    - 'ts' - 'is_final_success' - 'id'
FROM _g2_missing m
ON CONFLICT (request_id) DO NOTHING;

-- 结果反馈：登记行数与当前 pending 深度
SELECT 'backfill_rows' AS k, count(*)::text AS v
FROM public.session_mirror_outbox WHERE source = 'backfill'
UNION ALL
SELECT 'pending_now', count(*)::text
FROM public.session_mirror_outbox WHERE status = 'pending';

COMMIT;
