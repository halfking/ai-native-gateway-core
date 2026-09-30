-- mirror_outbox_backfill.sql — GAP-2 历史回填（存储优化 v2 §4-S4 前置项②）
--
-- 把近 N 天（默认 7 天）v1 **终态**行中带会话头、且缺 session_turns 的行投影为
-- telemetry.RequestLogEntry 同形态 JSON，灌入 session_mirror_outbox
-- （source='backfill'），由网关内 reaper（sessions_v2.mirror_outbox_replay，
-- 默认开）无差别消化重放。重放逐 gate 复刻实时 hook：非终态行与内部回环行
-- 会被 reaper 跳过并删行，无需在此处预先排除。
--
-- 选取口径（2026-09-30 修订）：
--   原口径 `is_final_success IS TRUE` 只能捞到「抢到本会话最终成功标记」的
--   那一小撮。实测本机 35 天窗口内 success=true 的终态行里，329 行
--   is_final_success=false —— 这些是真业务轮次、hook 当时也镜像了，
--   却因为不是 final-success 候选而完全落在这条回填之外。
--   更关键的是：pre-712（2026-09-15 03:18）那 1,459 行 genuine_loss 的
--   is_final_success **无一为 true**（失败轮次本就抢不到该标记），
--   原口径对它们命中率是 0。现改为按「终态 + 有会话头 + 无 turns」选取。
--
-- ⚠️ payload 的 success 必须取 v1 的 success 列，**不能取 is_final_success**。
-- 后者是「本请求是否是该会话的最终成功轮」，不是「本请求是否成功」；
-- 真库实测 is_final_success=true 恒蕴含 success=true，所以对原口径等价，
-- 但一旦放宽到非 final-success 的轮次就会把成功请求标成失败。
--
-- 契约：payload 键名对齐 RequestLogEntry 的 json tag（v1 列与 tag 几乎同名，
-- 例外：ts → event_at）。重放走 entryToProcessedRequest 同一桥接 +
-- v2.Write（request_id 幂等）。
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
-- 回看窗口：编辑下方 \set days。pre-712 的 1,459 行欠账横跨 09-03~09-29，
-- 要一次清完需 \set days 35。
--
-- 注：is_terminal 只存在于父表（hot 无此列），两腿无法统一使用，
-- 故终态判据用 request_status / error_kind —— 与 hook.go:991
-- isTerminalFailure 的口径一致。

\set days 7

-- 全部物化包进单事务：ON COMMIT DROP 依赖事务边界（psql autocommit 会
-- 在每条 CREATE 后立刻提交并删除临时表）。
BEGIN;

\echo '== 1/5 物化 v1 hot 侧 =='
CREATE TEMP TABLE _g2_v1_hot ON COMMIT DROP AS
SELECT request_id, ts, tenant_id, gw_session_id, is_final_success, success,
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
       task_type, is_auto_request, auto_decision::text AS auto_decision, auto_confidence,
       request_type,
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
WHERE gw_session_id IS NOT NULL
  AND gw_session_id <> ''
  AND request_id IS NOT NULL
  AND (COALESCE(request_status, '') <> 'in_progress' OR COALESCE(error_kind, '') <> '')
  AND ts > now() - (:days || ' days')::interval;

\echo '== 2/5 物化 v1 父表侧 =='
CREATE TEMP TABLE _g2_v1_par ON COMMIT DROP AS
SELECT request_id, ts, tenant_id, gw_session_id, is_final_success, success,
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
       task_type, is_auto_request, auto_decision::text AS auto_decision, auto_confidence,
       request_type,
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
WHERE gw_session_id IS NOT NULL
  AND gw_session_id <> ''
  AND request_id IS NOT NULL
  AND (COALESCE(request_status, '') <> 'in_progress' OR COALESCE(error_kind, '') <> '')
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
WHERE NOT EXISTS (SELECT 1 FROM _g2_has_turn t WHERE t.request_id = v.request_id)
  -- 按设计排除的预筛（效率 + 幂等，非语义权威）。
  -- 不加这道筛时，35 天窗口会灌 38,147 行，其中 36,693 行是网关内部回环：
  -- reaper 会按 hook 的同一判据跳过并删行，但它们永远不会有 turns，
  -- 于是每次重跑都重新灌一遍，ON CONFLICT DO NOTHING 救不回来。
  --
  -- 语义权威在 reaper（replay.go 逐 gate 复刻 hook）。本筛若因 Go 闸门演进
  -- 而变窄，失效方向是「多灌一批被 reaper 丢弃的行」——退化成上面那种
  -- 无害 churn，**不会漏回填**，所以这个方向是安全的。
  --
  -- 判据逐条对 IsInternalAutoEntry（domains/hooks/observability/telemetry/
  -- internal_loopback.go:23-39）：is_auto_request 必为 TRUE，且
  -- request_type / origin_actor / task_type 三者之一命中。work_type 不参与，
  -- 因为 Go 闸门根本不读它——按它筛会误杀业务轮次。
  AND NOT (COALESCE(v.is_auto_request, false)
       AND (COALESCE(TRIM(v.request_type), '') IN ('title_gen', 'summary')
         OR COALESCE(TRIM(v.origin_actor), '') IN ('auto-title-generator',
                                                    'auto-summary-generator',
                                                    'session-summary')
         OR COALESCE(TRIM(v.task_type), '') = ''));

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
        'success',            m.v1json -> 'success',
        'request_body',       CASE WHEN m.request_body  IS NULL THEN NULL ELSE m.request_body::text  END,
        'response_body',      CASE WHEN m.response_body IS NULL THEN NULL ELSE m.response_body::text END,
        'outbound_body',      m.outbound_body
    )
    - 'ts' - 'is_final_success' - 'id'
FROM _g2_missing m
-- 幂等：pending/claimed 行不重复灌。但 dead 必须能被重新驱动 —— 否则
-- 一次解码失败就会把该 request_id 永久钉死在 outbox 里，重跑本脚本
-- 永远救不回来（2026-09-30 实测踩中：auto_decision 是 jsonb 而
-- RequestLogEntry.AutoDecision 是 *string，12 行 decode 失败 dead-letter，
-- DO NOTHING 让修复后的重跑完全无效）。
ON CONFLICT (request_id) DO UPDATE
SET payload    = EXCLUDED.payload,
    session_id = EXCLUDED.session_id,
    tenant_id  = EXCLUDED.tenant_id,
    status     = 'pending',
    attempts   = 0,
    last_error = NULL,
    next_retry_at = NOW(),
    updated_at = NOW()
WHERE session_mirror_outbox.status = 'dead';

-- 结果反馈：登记行数与当前 pending 深度
SELECT 'backfill_rows' AS k, count(*)::text AS v
FROM public.session_mirror_outbox WHERE source = 'backfill'
UNION ALL
SELECT 'pending_now', count(*)::text
FROM public.session_mirror_outbox WHERE status = 'pending';

COMMIT;
