-- ===========================================================================
-- File:          sql/migrations/startup/885_drop_request_logs_v1_family.down.sql
-- Migration:     885 DOWN
-- Purpose:       把 v1 `request_logs` 族**骨架**装回去（表 + 序列 + 视图）。
--
-- ⚠⚠⚠ 这不是「零成本回滚」，请先读完再决定：
--
--   1. **数据不可恢复。** 885 删掉的是 v1 族里的**行**；本 down 只能重建
--      **结构**（空表 + 视图），救不回任何一行。要救数据，唯一路径是
--      **在 885 之前**把 v1 族归档到别处——这正是 885 的非空闸存在的理由：
--      族非空时它直接拒绝执行。
--
--   2. 恢复的是**改名后的那一代**（`_request_logs*`）。885 只会跑在 860
--      之后 ⇒ 回滚目标永远是 `_request_logs*` 代际 ⇒ 名字写死，不依赖投递
--      通道的词法改写（改写只认 `request_logs` 整词，`_request_logs` 不动）。
--
--   3. **分区不在本 down 的范围内。** 恢复出来的是母表（无分区）。分区由
--      分区管理任务在下一轮补齐——那些函数**也**被 885 删了，所以本 down
--      一并把它们装回去（见文件末尾）。
--
--   4. canonical 视图按 **885 执行前的真库定义**逐字还原（三臂体，含
--      710/734/813/815/816/817/824 的全部投影细节）。⚠ 注意：这**依赖两张
--      中层包装视图**（`request_logs_with_current_month_without_customer_id` /
--      `..._without_request_class_due_at`，577/610 建）——它们在正典里**没有
--      DDL 文件**⇒ 本 down 也一并按 577/610 的形态重建它们（见文件末尾）。
--
--   5. ⚠ 正典 `sql/objects/views/request_logs_with_current_month.sql` **已陈旧**：
--      它引用了活表已无的列（`request_type`）。⇒ 本 down 的三张视图**一律
--      取自真库定义**，不用正典那份。（这是一个可复用的教训：正典 objects 层
--      是**静态分析用**的 SSOT，不是部署形态的 SSOT。）
--
-- 幂等：表/序列用 IF NOT EXISTS；视图用 CREATE OR REPLACE。
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '10s';
SET LOCAL statement_timeout = '300s';

-- ── 序列 ────────────────────────────────────────────────────────────────
--
-- Name: _request_logs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE IF NOT EXISTS public._request_logs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;;

-- ── v1 母表（分区父表） ────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public._request_logs (
    id bigint DEFAULT nextval('_request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    client_endpoint text,
    client_timeout boolean,
    stream_chunk_errors integer,
    stream_chunks_sent integer DEFAULT 0 NOT NULL,
    client_request_id text,
    upstream_status_code integer,
    test_col text[] DEFAULT '{}'::text[] NOT NULL,
    test_tab_indent text,
    provider_model text,
    attachments jsonb,
    has_attachments boolean,
    attachment_count integer,
    client_ip inet,
    client_forwarded_for text,
    agent_name character varying(255),
    agent_type character varying(50),
    api_key_fingerprint character varying(16),
    customer_id bigint,
    upstream_endpoint text,
    session_title text,
    session_summary text,
    task_id character varying(255),
    task_title text,
    compression_start_index integer,
    compression_end_index integer,
    compression_ratio double precision,
    cache_hit boolean,
    cache_tokens_saved integer,
    content_safety_score jsonb,
    dlp_violations jsonb,
    sensitive_keywords text[],
    rate_limit_status character varying(50),
    client_protocol character varying(50),
    upstream_protocol character varying(50),
    protocol_conversion boolean,
    ir_extensions jsonb,
    sanitizer_mutations jsonb,
    vendor_metadata jsonb,
    reasoning_tokens integer,
    image_tokens integer,
    audio_tokens integer,
    video_tokens integer,
    provider_tokens integer,
    origin_stage character varying(32) DEFAULT NULL::character varying,
    origin_actor character varying(255) DEFAULT NULL::character varying,
    trace_events jsonb,
    routing_attempts jsonb,
    routing_summary text,
    effective_timeout_seconds integer,
    context_size_tokens integer,
    timeout_mode character varying(50),
    is_continuation boolean DEFAULT false,
    continuation_keywords text[],
    node_switch_count integer DEFAULT 0,
    keepalive_sent_count integer DEFAULT 0,
    cached_response_id bigint,
    canonical_model text,
    discard_events jsonb,
    request_class text DEFAULT 'immediate'::text NOT NULL,
    due_at timestamp with time zone,
    is_final_success boolean DEFAULT false NOT NULL,
    raw_model_name text,
    system_fingerprint text,
    t0_arrived_at timestamp with time zone,
    t1_total_enqueued_at timestamp with time zone,
    t2_total_dequeued_at timestamp with time zone,
    t3_model_enqueued_at timestamp with time zone,
    t4_model_dequeued_at timestamp with time zone,
    t5_cred_enqueued_at timestamp with time zone,
    t6_cred_dequeued_at timestamp with time zone,
    t7_forward_start_at timestamp with time zone,
    t8_response_start_at timestamp with time zone,
    t9_response_end_at timestamp with time zone,
    request_type text DEFAULT 'main'::text NOT NULL,
    credits_rate_multiplier double precision,
    token_band text,
    audio_seconds double precision
);

-- ── v1 hot 表 ───────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public._request_logs_hot (
    id bigint DEFAULT nextval('_request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    client_endpoint text,
    client_timeout boolean,
    stream_chunk_errors integer,
    stream_chunks_sent integer DEFAULT 0 NOT NULL,
    client_request_id text,
    upstream_status_code integer,
    test_col text[] DEFAULT '{}'::text[] NOT NULL,
    test_tab_indent text,
    provider_model text,
    attachments jsonb,
    has_attachments boolean DEFAULT false,
    attachment_count integer DEFAULT 0,
    client_ip inet,
    caller_id text,
    session_correlation_id text,
    compression_start_index integer,
    compression_end_index integer,
    client_forwarded_for text,
    agent_name character varying(255),
    agent_type character varying(50),
    api_key_fingerprint character varying(16),
    customer_id bigint,
    upstream_endpoint text,
    session_title text,
    session_summary text,
    task_id character varying(255),
    task_title text,
    protocol_conversion boolean,
    ir_extensions jsonb,
    sanitizer_mutations jsonb,
    compression_ratio double precision,
    cache_hit boolean,
    cache_tokens_saved integer,
    content_safety_score jsonb,
    dlp_violations jsonb,
    sensitive_keywords text[],
    vendor_metadata jsonb,
    client_protocol character varying(50),
    rate_limit_status character varying(50),
    upstream_protocol character varying(50),
    reasoning_tokens integer,
    image_tokens integer,
    audio_tokens integer,
    video_tokens integer,
    provider_tokens integer,
    origin_stage character varying(32) DEFAULT NULL::character varying,
    origin_actor character varying(255) DEFAULT NULL::character varying,
    routing_attempts jsonb,
    routing_summary text,
    trace_events jsonb,
    canonical_model text,
    discard_events jsonb,
    request_class text DEFAULT 'immediate'::text NOT NULL,
    due_at timestamp with time zone,
    is_final_success boolean DEFAULT false NOT NULL,
    status_code integer,
    system_fingerprint text,
    raw_model_name text,
    t0_arrived_at timestamp with time zone,
    t1_total_enqueued_at timestamp with time zone,
    t2_total_dequeued_at timestamp with time zone,
    t3_model_enqueued_at timestamp with time zone,
    t4_model_dequeued_at timestamp with time zone,
    t5_cred_enqueued_at timestamp with time zone,
    t6_cred_dequeued_at timestamp with time zone,
    t7_forward_start_at timestamp with time zone,
    t8_response_start_at timestamp with time zone,
    t9_response_end_at timestamp with time zone,
    request_type text DEFAULT 'main'::text NOT NULL,
    credits_rate_multiplier double precision,
    token_band text,
    audio_seconds double precision
);

-- ── v1 bodies 母表 ───────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public._request_logs_bodies (
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    request_body jsonb,
    outbound_body jsonb,
    response_body jsonb
);

-- ── v1 bodies hot 表 ────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public._request_logs_bodies_hot (
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    request_body jsonb,
    outbound_body jsonb,
    response_body jsonb
);

-- ── v1 archive 母表 ──────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public._request_logs_archive (
    id bigint NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_chunks_sent integer DEFAULT 0 NOT NULL,
    stream_chunk_errors integer,
    stream_done_sent boolean,
    client_timeout boolean,
    client_endpoint text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    stream_interrupted boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    client_request_id text
);

-- ── bodies 进度视图（取自**真库**的定义：正典那份已陈旧，引用了活表已无的列）─
CREATE OR REPLACE VIEW public.request_logs_bodies_progress AS
SELECT ( SELECT count(*) AS count
           FROM _request_logs
          WHERE _request_logs.request_body IS NOT NULL OR _request_logs.outbound_body IS NOT NULL OR _request_logs.response_body IS NOT NULL) AS source_rows_with_body,
    ( SELECT count(*) AS count
           FROM _request_logs_bodies) AS bodies_rows,
    ( SELECT count(*) AS count
           FROM _request_logs
          WHERE (_request_logs.request_body IS NOT NULL OR _request_logs.outbound_body IS NOT NULL OR _request_logs.response_body IS NOT NULL) AND NOT (EXISTS ( SELECT 1
                   FROM _request_logs_bodies b
                  WHERE b.request_id = _request_logs.request_id AND b.ts = _request_logs.ts))) AS rows_pending_backfill;;

-- ── bodies 当前月视图（取自真库的定义）─────────────────────────────────────
CREATE OR REPLACE VIEW public.request_logs_bodies_with_current_month AS
SELECT _request_logs_bodies_hot.request_id,
    _request_logs_bodies_hot.ts,
    _request_logs_bodies_hot.request_body,
    _request_logs_bodies_hot.outbound_body,
    _request_logs_bodies_hot.response_body
   FROM _request_logs_bodies_hot
UNION ALL
 SELECT _request_logs_bodies.request_id,
    _request_logs_bodies.ts,
    _request_logs_bodies.request_body,
    _request_logs_bodies.outbound_body,
    _request_logs_bodies.response_body
   FROM _request_logs_bodies;;

-- ── 中层包装视图（577/610 形态；正典里**没有**这两张的 DDL 文件）───────────
--    canonical 三臂体依赖 `..._without_request_class_due_at`，它又依赖
--    `..._without_customer_id` ⇒ 顺序不能反。
CREATE OR REPLACE VIEW public.request_logs_with_current_month_without_customer_id AS
SELECT _request_logs_hot.id,
    _request_logs_hot.request_id,
    _request_logs_hot.ts,
    _request_logs_hot.tenant_id,
    _request_logs_hot.application_id,
    _request_logs_hot.api_key_id,
    _request_logs_hot.end_user_id,
    _request_logs_hot.client_model,
    _request_logs_hot.outbound_model,
    _request_logs_hot.credential_id,
    _request_logs_hot.provider_id,
    _request_logs_hot.canonical_id,
    _request_logs_hot.client_profile,
    _request_logs_hot.request_mode,
    _request_logs_hot.prompt_tokens,
    _request_logs_hot.completion_tokens,
    _request_logs_hot.total_tokens,
    _request_logs_hot.cost_usd,
    _request_logs_hot.latency_ms,
    _request_logs_hot.success,
    _request_logs_hot.error_kind,
    _request_logs_hot.search_text,
    _request_logs_hot.cache_read_tokens,
    _request_logs_hot.cache_write_tokens,
    _request_logs_hot.identity_hash,
    _request_logs_hot.virtual_client_id,
    _request_logs_hot.virtual_ip,
    _request_logs_hot.virtual_mac,
    _request_logs_hot.affinity_hit,
    _request_logs_hot.stream_first_chunk_ms,
    _request_logs_hot.stream_chunk_count,
    _request_logs_hot.stream_interrupted,
    _request_logs_hot.stream_done_sent,
    _request_logs_hot.request_checksum,
    _request_logs_hot.response_checksum,
    _request_logs_hot.transform_rule_id,
    _request_logs_hot.egress_protocol,
    _request_logs_hot.failure_stage,
    _request_logs_hot.failure_detail_code,
    _request_logs_hot.request_preview,
    _request_logs_hot.transform_summary,
    _request_logs_hot.response_preview,
    _request_logs_hot.stream_done_received,
    _request_logs_hot.request_body,
    _request_logs_hot.response_body,
    _request_logs_hot.cost_display,
    _request_logs_hot.cost_currency,
    _request_logs_hot.usage_source,
    _request_logs_hot.gw_session_id,
    _request_logs_hot.gw_task_id,
    _request_logs_hot.request_status,
    _request_logs_hot.api_key_prefix,
    _request_logs_hot.owner_user,
    _request_logs_hot.application_code,
    _request_logs_hot.key_alias,
    _request_logs_hot.api_key_owner_user,
    _request_logs_hot.is_auto_request,
    _request_logs_hot.task_type,
    _request_logs_hot.auto_profile,
    _request_logs_hot.auto_decision,
    _request_logs_hot.auto_confidence,
    _request_logs_hot.work_type,
    _request_logs_hot.task_type_chosen,
    _request_logs_hot.confidence_num,
    _request_logs_hot.model_chosen,
    _request_logs_hot.strategy_used,
    _request_logs_hot.credits_charged,
    _request_logs_hot.parent_request_id,
    _request_logs_hot.compression_reason,
    _request_logs_hot.compression_strategy,
    _request_logs_hot.compression_meta,
    _request_logs_hot.outbound_body,
    _request_logs_hot.outbound_msg_count,
    _request_logs_hot.outbound_token_est,
    _request_logs_hot.outbound_msg_hashes,
    _request_logs_hot.quality_flags,
    _request_logs_hot.quality_fix_actions,
    _request_logs_hot.quality_score,
    _request_logs_hot.upstream_finish_reason,
    _request_logs_hot.tool_calls,
    _request_logs_hot.client_endpoint,
    _request_logs_hot.client_timeout,
    _request_logs_hot.stream_chunk_errors,
    _request_logs_hot.stream_chunks_sent,
    _request_logs_hot.client_request_id,
    _request_logs_hot.upstream_status_code,
    _request_logs_hot.test_col,
    _request_logs_hot.test_tab_indent,
    _request_logs_hot.provider_model,
    _request_logs_hot.attachments,
    _request_logs_hot.has_attachments,
    _request_logs_hot.attachment_count,
    _request_logs_hot.client_ip,
    _request_logs_hot.compression_start_index,
    _request_logs_hot.compression_end_index,
    _request_logs_hot.client_forwarded_for,
    _request_logs_hot.agent_name,
    _request_logs_hot.agent_type,
    _request_logs_hot.api_key_fingerprint,
    _request_logs_hot.upstream_endpoint,
    _request_logs_hot.session_title,
    _request_logs_hot.session_summary,
    _request_logs_hot.task_id,
    _request_logs_hot.task_title,
    _request_logs_hot.protocol_conversion,
    _request_logs_hot.ir_extensions,
    _request_logs_hot.sanitizer_mutations,
    _request_logs_hot.compression_ratio,
    _request_logs_hot.cache_hit,
    _request_logs_hot.cache_tokens_saved,
    _request_logs_hot.content_safety_score,
    _request_logs_hot.dlp_violations,
    _request_logs_hot.sensitive_keywords,
    _request_logs_hot.vendor_metadata,
    _request_logs_hot.client_protocol,
    _request_logs_hot.rate_limit_status,
    _request_logs_hot.upstream_protocol,
    _request_logs_hot.reasoning_tokens,
    _request_logs_hot.image_tokens,
    _request_logs_hot.audio_tokens,
    _request_logs_hot.video_tokens,
    _request_logs_hot.provider_tokens,
    _request_logs_hot.origin_stage,
    _request_logs_hot.origin_actor,
    _request_logs_hot.routing_attempts,
    _request_logs_hot.routing_summary,
    _request_logs_hot.trace_events,
    _request_logs_hot.canonical_model,
    _request_logs_hot.discard_events,
    _request_logs_hot.is_final_success,
    _request_logs_hot.system_fingerprint,
    _request_logs_hot.raw_model_name,
    _request_logs_hot.t0_arrived_at,
    _request_logs_hot.t1_total_enqueued_at,
    _request_logs_hot.t2_total_dequeued_at,
    _request_logs_hot.t3_model_enqueued_at,
    _request_logs_hot.t4_model_dequeued_at,
    _request_logs_hot.t5_cred_enqueued_at,
    _request_logs_hot.t6_cred_dequeued_at,
    _request_logs_hot.t7_forward_start_at,
    _request_logs_hot.t8_response_start_at,
    _request_logs_hot.t9_response_end_at,
    _request_logs_hot.request_type,
    _request_logs_hot.credits_rate_multiplier
   FROM _request_logs_hot
UNION ALL
 SELECT _request_logs.id,
    _request_logs.request_id,
    _request_logs.ts,
    _request_logs.tenant_id,
    _request_logs.application_id,
    _request_logs.api_key_id,
    _request_logs.end_user_id,
    _request_logs.client_model,
    _request_logs.outbound_model,
    _request_logs.credential_id,
    _request_logs.provider_id,
    _request_logs.canonical_id,
    _request_logs.client_profile,
    _request_logs.request_mode,
    _request_logs.prompt_tokens,
    _request_logs.completion_tokens,
    _request_logs.total_tokens,
    _request_logs.cost_usd,
    _request_logs.latency_ms,
    _request_logs.success,
    _request_logs.error_kind,
    _request_logs.search_text,
    _request_logs.cache_read_tokens,
    _request_logs.cache_write_tokens,
    _request_logs.identity_hash,
    _request_logs.virtual_client_id,
    _request_logs.virtual_ip,
    _request_logs.virtual_mac,
    _request_logs.affinity_hit,
    _request_logs.stream_first_chunk_ms,
    _request_logs.stream_chunk_count,
    _request_logs.stream_interrupted,
    _request_logs.stream_done_sent,
    _request_logs.request_checksum,
    _request_logs.response_checksum,
    _request_logs.transform_rule_id,
    _request_logs.egress_protocol,
    _request_logs.failure_stage,
    _request_logs.failure_detail_code,
    _request_logs.request_preview,
    _request_logs.transform_summary,
    _request_logs.response_preview,
    _request_logs.stream_done_received,
    _request_logs.request_body,
    _request_logs.response_body,
    _request_logs.cost_display,
    _request_logs.cost_currency,
    _request_logs.usage_source,
    _request_logs.gw_session_id,
    _request_logs.gw_task_id,
    _request_logs.request_status,
    _request_logs.api_key_prefix,
    _request_logs.owner_user,
    _request_logs.application_code,
    _request_logs.key_alias,
    _request_logs.api_key_owner_user,
    _request_logs.is_auto_request,
    _request_logs.task_type,
    _request_logs.auto_profile,
    _request_logs.auto_decision,
    _request_logs.auto_confidence,
    _request_logs.work_type,
    _request_logs.task_type_chosen,
    _request_logs.confidence_num,
    _request_logs.model_chosen,
    _request_logs.strategy_used,
    _request_logs.credits_charged,
    _request_logs.parent_request_id,
    _request_logs.compression_reason,
    _request_logs.compression_strategy,
    _request_logs.compression_meta,
    _request_logs.outbound_body,
    _request_logs.outbound_msg_count,
    _request_logs.outbound_token_est,
    _request_logs.outbound_msg_hashes,
    _request_logs.quality_flags,
    _request_logs.quality_fix_actions,
    _request_logs.quality_score,
    _request_logs.upstream_finish_reason,
    _request_logs.tool_calls,
    _request_logs.client_endpoint,
    _request_logs.client_timeout,
    _request_logs.stream_chunk_errors,
    _request_logs.stream_chunks_sent,
    _request_logs.client_request_id,
    _request_logs.upstream_status_code,
    _request_logs.test_col,
    _request_logs.test_tab_indent,
    _request_logs.provider_model,
    _request_logs.attachments,
    _request_logs.has_attachments,
    _request_logs.attachment_count,
    _request_logs.client_ip,
    _request_logs.compression_start_index,
    _request_logs.compression_end_index,
    _request_logs.client_forwarded_for,
    _request_logs.agent_name,
    _request_logs.agent_type,
    _request_logs.api_key_fingerprint,
    _request_logs.upstream_endpoint,
    _request_logs.session_title,
    _request_logs.session_summary,
    _request_logs.task_id,
    _request_logs.task_title,
    _request_logs.protocol_conversion,
    _request_logs.ir_extensions,
    _request_logs.sanitizer_mutations,
    _request_logs.compression_ratio,
    _request_logs.cache_hit,
    _request_logs.cache_tokens_saved,
    _request_logs.content_safety_score,
    _request_logs.dlp_violations,
    _request_logs.sensitive_keywords,
    _request_logs.vendor_metadata,
    _request_logs.client_protocol,
    _request_logs.rate_limit_status,
    _request_logs.upstream_protocol,
    _request_logs.reasoning_tokens,
    _request_logs.image_tokens,
    _request_logs.audio_tokens,
    _request_logs.video_tokens,
    _request_logs.provider_tokens,
    _request_logs.origin_stage,
    _request_logs.origin_actor,
    _request_logs.routing_attempts,
    _request_logs.routing_summary,
    _request_logs.trace_events,
    _request_logs.canonical_model,
    _request_logs.discard_events,
    _request_logs.is_final_success,
    _request_logs.system_fingerprint,
    _request_logs.raw_model_name,
    _request_logs.t0_arrived_at,
    _request_logs.t1_total_enqueued_at,
    _request_logs.t2_total_dequeued_at,
    _request_logs.t3_model_enqueued_at,
    _request_logs.t4_model_dequeued_at,
    _request_logs.t5_cred_enqueued_at,
    _request_logs.t6_cred_dequeued_at,
    _request_logs.t7_forward_start_at,
    _request_logs.t8_response_start_at,
    _request_logs.t9_response_end_at,
    _request_logs.request_type,
    _request_logs.credits_rate_multiplier
   FROM _request_logs;;

CREATE OR REPLACE VIEW public.request_logs_with_current_month_without_request_class_due_at AS
SELECT v.id,
    v.request_id,
    v.ts,
    v.tenant_id,
    v.application_id,
    v.api_key_id,
    v.end_user_id,
    v.client_model,
    v.outbound_model,
    v.credential_id,
    v.provider_id,
    v.canonical_id,
    v.client_profile,
    v.request_mode,
    v.prompt_tokens,
    v.completion_tokens,
    v.total_tokens,
    v.cost_usd,
    v.latency_ms,
    v.success,
    v.error_kind,
    v.search_text,
    v.cache_read_tokens,
    v.cache_write_tokens,
    v.identity_hash,
    v.virtual_client_id,
    v.virtual_ip,
    v.virtual_mac,
    v.affinity_hit,
    v.stream_first_chunk_ms,
    v.stream_chunk_count,
    v.stream_interrupted,
    v.stream_done_sent,
    v.request_checksum,
    v.response_checksum,
    v.transform_rule_id,
    v.egress_protocol,
    v.failure_stage,
    v.failure_detail_code,
    v.request_preview,
    v.transform_summary,
    v.response_preview,
    v.stream_done_received,
    v.request_body,
    v.response_body,
    v.cost_display,
    v.cost_currency,
    v.usage_source,
    v.gw_session_id,
    v.gw_task_id,
    v.request_status,
    v.api_key_prefix,
    v.owner_user,
    v.application_code,
    v.key_alias,
    v.api_key_owner_user,
    v.is_auto_request,
    v.task_type,
    v.auto_profile,
    v.auto_decision,
    v.auto_confidence,
    v.work_type,
    v.task_type_chosen,
    v.confidence_num,
    v.model_chosen,
    v.strategy_used,
    v.credits_charged,
    v.parent_request_id,
    v.compression_reason,
    v.compression_strategy,
    v.compression_meta,
    v.outbound_body,
    v.outbound_msg_count,
    v.outbound_token_est,
    v.outbound_msg_hashes,
    v.quality_flags,
    v.quality_fix_actions,
    v.quality_score,
    v.upstream_finish_reason,
    v.tool_calls,
    v.client_endpoint,
    v.client_timeout,
    v.stream_chunk_errors,
    v.stream_chunks_sent,
    v.client_request_id,
    v.upstream_status_code,
    v.test_col,
    v.test_tab_indent,
    v.provider_model,
    v.attachments,
    v.has_attachments,
    v.attachment_count,
    v.client_ip,
    v.compression_start_index,
    v.compression_end_index,
    v.client_forwarded_for,
    v.agent_name,
    v.agent_type,
    v.api_key_fingerprint,
    v.upstream_endpoint,
    v.session_title,
    v.session_summary,
    v.task_id,
    v.task_title,
    v.protocol_conversion,
    v.ir_extensions,
    v.sanitizer_mutations,
    v.compression_ratio,
    v.cache_hit,
    v.cache_tokens_saved,
    v.content_safety_score,
    v.dlp_violations,
    v.sensitive_keywords,
    v.vendor_metadata,
    v.client_protocol,
    v.rate_limit_status,
    v.upstream_protocol,
    v.reasoning_tokens,
    v.image_tokens,
    v.audio_tokens,
    v.video_tokens,
    v.provider_tokens,
    v.origin_stage,
    v.origin_actor,
    v.routing_attempts,
    v.routing_summary,
    v.trace_events,
    v.canonical_model,
    v.discard_events,
    v.is_final_success,
    v.system_fingerprint,
    v.raw_model_name,
    v.t0_arrived_at,
    v.t1_total_enqueued_at,
    v.t2_total_dequeued_at,
    v.t3_model_enqueued_at,
    v.t4_model_dequeued_at,
    v.t5_cred_enqueued_at,
    v.t6_cred_dequeued_at,
    v.t7_forward_start_at,
    v.t8_response_start_at,
    v.t9_response_end_at,
    v.request_type,
    m.customer_id,
    v.credits_rate_multiplier
   FROM request_logs_with_current_month_without_customer_id v
     LEFT JOIN LATERAL ( SELECT _request_logs_hot.customer_id
           FROM _request_logs_hot
          WHERE _request_logs_hot.request_id = v.request_id AND _request_logs_hot.ts = v.ts
        UNION ALL
         SELECT _request_logs.customer_id
           FROM _request_logs
          WHERE _request_logs.request_id = v.request_id AND _request_logs.ts = v.ts
 LIMIT 1) m ON true;;

-- ── canonical 视图：**三臂体**（取自 885 执行前的真库定义，逐字还原）──────
CREATE OR REPLACE VIEW public.request_logs_with_current_month AS
SELECT NULL::bigint AS id,
    t.request_id,
    t.ts,
    t.tenant_id::text AS tenant_id,
        CASE
            WHEN t.application_id ~ '^[0-9]+$'::text THEN t.application_id::bigint
            ELSE NULL::bigint
        END AS application_id,
        CASE
            WHEN t.api_key_id ~ '^[0-9]+$'::text THEN t.api_key_id::bigint
            ELSE NULL::bigint
        END AS api_key_id,
    t.end_user_id,
    d.client_model,
    t.model AS outbound_model,
        CASE
            WHEN t.credential_id ~ '^[0-9]+$'::text THEN t.credential_id::bigint
            ELSE NULL::bigint
        END AS credential_id,
    d.provider_id,
    t.canonical_id,
    d.client_profile,
    t.submit_mode AS request_mode,
    t.prompt_tokens,
    t.completion_tokens,
    NULLIF(COALESCE(t.prompt_tokens, 0) + COALESCE(t.completion_tokens, 0), 0) AS total_tokens,
    t.cost_usd,
    t.latency_ms,
    t.success,
    t.error_kind,
    t.search_text,
    t.cache_read_tokens,
    t.cache_write_tokens,
    t.identity_hash,
    t.virtual_client_id,
    d.virtual_ip,
    d.virtual_mac,
    d.affinity_hit,
    t.stream_first_chunk_ms,
    t.stream_chunk_count,
    t.stream_interrupted,
    t.stream_done_sent,
    t.request_checksum,
    t.response_checksum,
    d.transform_rule_id,
    t.egress_protocol,
    t.failure_stage,
    t.failure_detail_code,
    t.request_preview,
    t.transform_summary,
    t.response_preview,
    t.stream_done_sent AS stream_done_received,
    t.cost_display::numeric(14,8) AS cost_display,
    t.cost_currency,
    t.usage_source,
        CASE
            WHEN t.session_id ~~ 'sys:%'::text THEN NULL::text
            ELSE t.session_id
        END AS gw_session_id,
    d.gw_task_id,
        CASE
            WHEN t.success IS NULL THEN NULL::text
            WHEN t.success THEN 'success'::text
            WHEN t.error_kind = ANY (ARRAY['rate_limit_exceeded'::text, 'key_throttled'::text]) THEN 'rate_limited'::text
            WHEN t.status_code = 429 THEN 'rate_limited'::text
            ELSE 'failure'::text
        END AS request_status,
    d.api_key_prefix,
    d.owner_user,
    d.application_code,
    d.key_alias,
    d.api_key_owner_user,
    t.is_auto_request,
    t.task_type,
    d.auto_profile,
        CASE
            WHEN NULLIF(t.auto_decision, ''::text) IS NOT NULL THEN t.auto_decision::jsonb
            ELSE NULL::jsonb
        END AS auto_decision,
    t.auto_confidence::numeric(4,3) AS auto_confidence,
    t.work_type,
    t.task_type_chosen,
    d.confidence_num,
    d.model_chosen,
    d.strategy_used,
    t.credits_charged,
    t.parent_request_id,
    d.compression_reason,
    t.compression_strategy,
    t.compression_meta,
    d.outbound_msg_count,
    d.outbound_token_est,
    d.outbound_msg_hashes,
    d.quality_flags,
    d.quality_fix_actions,
    d.quality_score,
    t.upstream_finish_reason,
    t.tools AS tool_calls,
    t.client_endpoint,
    t.client_timeout,
    d.stream_chunk_errors,
    d.stream_chunks_sent,
    t.client_request_id,
    t.upstream_status_code,
    NULL::text[] AS test_col,
    NULL::text AS test_tab_indent,
    NULL::text AS provider_model,
    d.attachments,
        CASE
            WHEN t.attachment_count IS NULL THEN NULL::boolean
            WHEN t.attachment_count > 0 THEN true
            ELSE false
        END AS has_attachments,
    t.attachment_count,
    t.routing_attempts,
    t.routing_summary,
    t.agent_name,
    t.agent_type,
    t.client_protocol::character varying(50) AS client_protocol,
    t.canonical_model,
    t.t0_arrived_at,
    t.t1_total_enqueued_at,
    t.t2_total_dequeued_at,
    t.t3_model_enqueued_at,
    t.t4_model_dequeued_at,
    t.t5_cred_enqueued_at,
    t.t6_cred_dequeued_at,
    t.t7_forward_start_at,
    t.t8_response_start_at,
    t.t9_response_end_at,
    d.request_type,
    t.is_final_success,
    t.origin_actor::character varying(255) AS origin_actor,
    t.customer_id,
    d.request_class,
    d.due_at,
    t.system_fingerprint,
    t.raw_model_name,
    NULL::double precision AS credits_rate_multiplier,
        CASE
            WHEN pg_input_is_valid(t.client_ip, 'inet'::text) THEN t.client_ip::inet
            ELSE NULL::inet
        END AS client_ip,
    t.origin_stage,
    t.token_band,
    t.client_forwarded_for
   FROM session_turns_hot t
     LEFT JOIN session_turn_details_hot d ON d.tenant_id::text = t.tenant_id::text AND d.request_id = t.request_id AND d.partition_date = t.partition_date
UNION ALL
 SELECT NULL::bigint AS id,
    t.request_id,
    t.ts,
    t.tenant_id::text AS tenant_id,
        CASE
            WHEN t.application_id ~ '^[0-9]+$'::text THEN t.application_id::bigint
            ELSE NULL::bigint
        END AS application_id,
        CASE
            WHEN t.api_key_id ~ '^[0-9]+$'::text THEN t.api_key_id::bigint
            ELSE NULL::bigint
        END AS api_key_id,
    t.end_user_id,
    d.client_model,
    t.model AS outbound_model,
        CASE
            WHEN t.credential_id ~ '^[0-9]+$'::text THEN t.credential_id::bigint
            ELSE NULL::bigint
        END AS credential_id,
    d.provider_id,
    t.canonical_id,
    d.client_profile,
    t.submit_mode AS request_mode,
    t.prompt_tokens,
    t.completion_tokens,
    NULLIF(COALESCE(t.prompt_tokens, 0) + COALESCE(t.completion_tokens, 0), 0) AS total_tokens,
    t.cost_usd,
    t.latency_ms,
    t.success,
    t.error_kind,
    t.search_text,
    t.cache_read_tokens,
    t.cache_write_tokens,
    t.identity_hash,
    t.virtual_client_id,
    d.virtual_ip,
    d.virtual_mac,
    d.affinity_hit,
    t.stream_first_chunk_ms,
    t.stream_chunk_count,
    t.stream_interrupted,
    t.stream_done_sent,
    t.request_checksum,
    t.response_checksum,
    d.transform_rule_id,
    t.egress_protocol,
    t.failure_stage,
    t.failure_detail_code,
    t.request_preview,
    t.transform_summary,
    t.response_preview,
    t.stream_done_sent AS stream_done_received,
    t.cost_display::numeric(14,8) AS cost_display,
    t.cost_currency,
    t.usage_source,
        CASE
            WHEN t.session_id ~~ 'sys:%'::text THEN NULL::text
            ELSE t.session_id
        END AS gw_session_id,
    d.gw_task_id,
        CASE
            WHEN t.success IS NULL THEN NULL::text
            WHEN t.success THEN 'success'::text
            WHEN t.error_kind = ANY (ARRAY['rate_limit_exceeded'::text, 'key_throttled'::text]) THEN 'rate_limited'::text
            WHEN t.status_code = 429 THEN 'rate_limited'::text
            ELSE 'failure'::text
        END AS request_status,
    d.api_key_prefix,
    d.owner_user,
    d.application_code,
    d.key_alias,
    d.api_key_owner_user,
    t.is_auto_request,
    t.task_type,
    d.auto_profile,
        CASE
            WHEN NULLIF(t.auto_decision, ''::text) IS NOT NULL THEN t.auto_decision::jsonb
            ELSE NULL::jsonb
        END AS auto_decision,
    t.auto_confidence::numeric(4,3) AS auto_confidence,
    t.work_type,
    t.task_type_chosen,
    d.confidence_num,
    d.model_chosen,
    d.strategy_used,
    t.credits_charged,
    t.parent_request_id,
    d.compression_reason,
    t.compression_strategy,
    t.compression_meta,
    d.outbound_msg_count,
    d.outbound_token_est,
    d.outbound_msg_hashes,
    d.quality_flags,
    d.quality_fix_actions,
    d.quality_score,
    t.upstream_finish_reason,
    t.tools AS tool_calls,
    t.client_endpoint,
    t.client_timeout,
    d.stream_chunk_errors,
    d.stream_chunks_sent,
    t.client_request_id,
    t.upstream_status_code,
    NULL::text[] AS test_col,
    NULL::text AS test_tab_indent,
    NULL::text AS provider_model,
    d.attachments,
        CASE
            WHEN t.attachment_count IS NULL THEN NULL::boolean
            WHEN t.attachment_count > 0 THEN true
            ELSE false
        END AS has_attachments,
    t.attachment_count,
    t.routing_attempts,
    t.routing_summary,
    t.agent_name,
    t.agent_type,
    t.client_protocol::character varying(50) AS client_protocol,
    t.canonical_model,
    t.t0_arrived_at,
    t.t1_total_enqueued_at,
    t.t2_total_dequeued_at,
    t.t3_model_enqueued_at,
    t.t4_model_dequeued_at,
    t.t5_cred_enqueued_at,
    t.t6_cred_dequeued_at,
    t.t7_forward_start_at,
    t.t8_response_start_at,
    t.t9_response_end_at,
    d.request_type,
    t.is_final_success,
    t.origin_actor::character varying(255) AS origin_actor,
    t.customer_id,
    d.request_class,
    d.due_at,
    t.system_fingerprint,
    t.raw_model_name,
    NULL::double precision AS credits_rate_multiplier,
        CASE
            WHEN pg_input_is_valid(t.client_ip, 'inet'::text) THEN t.client_ip::inet
            ELSE NULL::inet
        END AS client_ip,
    t.origin_stage,
    t.token_band,
    t.client_forwarded_for
   FROM session_turns t
     LEFT JOIN session_turn_details d ON d.tenant_id::text = t.tenant_id::text AND d.request_id = t.request_id AND d.partition_date = t.partition_date
UNION ALL
 SELECT rl.id,
    rl.request_id,
    rl.ts,
    rl.tenant_id,
    rl.application_id,
    rl.api_key_id,
    rl.end_user_id,
    rl.client_model,
    rl.outbound_model,
    rl.credential_id,
    rl.provider_id,
    rl.canonical_id,
    rl.client_profile,
    rl.request_mode,
    rl.prompt_tokens,
    rl.completion_tokens,
    rl.total_tokens,
    rl.cost_usd,
    rl.latency_ms,
    rl.success,
    rl.error_kind,
    rl.search_text,
    rl.cache_read_tokens,
    rl.cache_write_tokens,
    rl.identity_hash,
    rl.virtual_client_id,
    rl.virtual_ip,
    rl.virtual_mac,
    rl.affinity_hit,
    rl.stream_first_chunk_ms,
    rl.stream_chunk_count,
    rl.stream_interrupted,
    rl.stream_done_sent,
    rl.request_checksum,
    rl.response_checksum,
    rl.transform_rule_id,
    rl.egress_protocol,
    rl.failure_stage,
    rl.failure_detail_code,
    rl.request_preview,
    rl.transform_summary,
    rl.response_preview,
    rl.stream_done_received,
    rl.cost_display,
    rl.cost_currency,
    rl.usage_source,
    rl.gw_session_id,
    rl.gw_task_id,
    rl.request_status,
    rl.api_key_prefix,
    rl.owner_user,
    rl.application_code,
    rl.key_alias,
    rl.api_key_owner_user,
    rl.is_auto_request,
    rl.task_type,
    rl.auto_profile,
    rl.auto_decision,
    rl.auto_confidence,
    rl.work_type,
    rl.task_type_chosen,
    rl.confidence_num,
    rl.model_chosen,
    rl.strategy_used,
    rl.credits_charged,
    rl.parent_request_id,
    rl.compression_reason,
    rl.compression_strategy,
    rl.compression_meta,
    rl.outbound_msg_count,
    rl.outbound_token_est,
    rl.outbound_msg_hashes,
    rl.quality_flags,
    rl.quality_fix_actions,
    rl.quality_score,
    rl.upstream_finish_reason,
    rl.tool_calls,
    rl.client_endpoint,
    rl.client_timeout,
    rl.stream_chunk_errors,
    rl.stream_chunks_sent,
    rl.client_request_id,
    rl.upstream_status_code,
    rl.test_col,
    rl.test_tab_indent,
    rl.provider_model,
    rl.attachments,
    rl.has_attachments,
    rl.attachment_count,
    rl.routing_attempts,
    rl.routing_summary,
    rl.agent_name,
    rl.agent_type,
    rl.client_protocol,
    rl.canonical_model,
    rl.t0_arrived_at,
    rl.t1_total_enqueued_at,
    rl.t2_total_dequeued_at,
    rl.t3_model_enqueued_at,
    rl.t4_model_dequeued_at,
    rl.t5_cred_enqueued_at,
    rl.t6_cred_dequeued_at,
    rl.t7_forward_start_at,
    rl.t8_response_start_at,
    rl.t9_response_end_at,
    rl.request_type,
    rl.is_final_success,
    rl.origin_actor,
    rl.customer_id,
    rl.request_class,
    rl.due_at,
    rl.system_fingerprint,
    rl.raw_model_name,
    rl.credits_rate_multiplier,
    rl.client_ip,
    rl.origin_stage,
    rl.token_band,
    rl.client_forwarded_for
   FROM ( SELECT v.id,
            v.request_id,
            v.ts,
            v.tenant_id,
            v.application_id,
            v.api_key_id,
            v.end_user_id,
            v.client_model,
            v.outbound_model,
            v.credential_id,
            v.provider_id,
            v.canonical_id,
            v.client_profile,
            v.request_mode,
            v.prompt_tokens,
            v.completion_tokens,
            v.total_tokens,
            v.cost_usd,
            v.latency_ms,
            v.success,
            v.error_kind,
            v.search_text,
            v.cache_read_tokens,
            v.cache_write_tokens,
            v.identity_hash,
            v.virtual_client_id,
            v.virtual_ip,
            v.virtual_mac,
            v.affinity_hit,
            v.stream_first_chunk_ms,
            v.stream_chunk_count,
            v.stream_interrupted,
            v.stream_done_sent,
            v.request_checksum,
            v.response_checksum,
            v.transform_rule_id,
            v.egress_protocol,
            v.failure_stage,
            v.failure_detail_code,
            v.request_preview,
            v.transform_summary,
            v.response_preview,
            v.stream_done_received,
            v.request_body,
            v.response_body,
            v.cost_display,
            v.cost_currency,
            v.usage_source,
            v.gw_session_id,
            v.gw_task_id,
            v.request_status,
            v.api_key_prefix,
            v.owner_user,
            v.application_code,
            v.key_alias,
            v.api_key_owner_user,
            v.is_auto_request,
            v.task_type,
            v.auto_profile,
            v.auto_decision,
            v.auto_confidence,
            v.work_type,
            v.task_type_chosen,
            v.confidence_num,
            v.model_chosen,
            v.strategy_used,
            v.credits_charged,
            v.parent_request_id,
            v.compression_reason,
            v.compression_strategy,
            v.compression_meta,
            v.outbound_body,
            v.outbound_msg_count,
            v.outbound_token_est,
            v.outbound_msg_hashes,
            v.quality_flags,
            v.quality_fix_actions,
            v.quality_score,
            v.upstream_finish_reason,
            v.tool_calls,
            v.client_endpoint,
            v.client_timeout,
            v.stream_chunk_errors,
            v.stream_chunks_sent,
            v.client_request_id,
            v.upstream_status_code,
            v.test_col,
            v.test_tab_indent,
            v.provider_model,
            v.attachments,
            v.has_attachments,
            v.attachment_count,
            v.compression_start_index,
            v.compression_end_index,
            v.client_forwarded_for,
            v.agent_name,
            v.agent_type,
            v.api_key_fingerprint,
            v.upstream_endpoint,
            v.session_title,
            v.session_summary,
            v.task_id,
            v.task_title,
            v.protocol_conversion,
            v.ir_extensions,
            v.sanitizer_mutations,
            v.compression_ratio,
            v.cache_hit,
            v.cache_tokens_saved,
            v.content_safety_score,
            v.dlp_violations,
            v.sensitive_keywords,
            v.vendor_metadata,
            v.client_protocol,
            v.rate_limit_status,
            v.upstream_protocol,
            v.reasoning_tokens,
            v.image_tokens,
            v.audio_tokens,
            v.video_tokens,
            v.provider_tokens,
            v.origin_stage,
            v.origin_actor,
            v.routing_attempts,
            v.routing_summary,
            v.trace_events,
            v.canonical_model,
            v.discard_events,
            v.is_final_success,
            v.system_fingerprint,
            v.raw_model_name,
            v.t0_arrived_at,
            v.t1_total_enqueued_at,
            v.t2_total_dequeued_at,
            v.t3_model_enqueued_at,
            v.t4_model_dequeued_at,
            v.t5_cred_enqueued_at,
            v.t6_cred_dequeued_at,
            v.t7_forward_start_at,
            v.t8_response_start_at,
            v.t9_response_end_at,
            v.request_type,
            v.customer_id,
            source.request_class,
            source.due_at,
            source.token_band,
            v.credits_rate_multiplier,
            v.client_ip
           FROM request_logs_with_current_month_without_request_class_due_at v
             LEFT JOIN LATERAL ( SELECT h.request_class,
                    h.due_at,
                    h.token_band
                   FROM _request_logs_hot h
                  WHERE h.request_id = v.request_id AND h.ts = v.ts
                UNION ALL
                 SELECT p.request_class,
                    p.due_at,
                    p.token_band
                   FROM _request_logs p
                  WHERE p.request_id = v.request_id AND p.ts = v.ts
         LIMIT 1) source ON true) rl
  WHERE NOT (EXISTS ( SELECT 1
           FROM session_turns_hot th
          WHERE th.request_id = rl.request_id)) AND NOT (EXISTS ( SELECT 1
           FROM session_turns tp
          WHERE tp.request_id = rl.request_id));;

-- ── 分区管理函数（885 一并删掉的那些）──────────────────────────────────

-- ── 分区管理函数：ensure_request_logs_partition_timestamp_with_time_zone.sql ────────────────────────────────────────────────
--
-- Name: ensure_request_logs_partition(timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.ensure_request_logs_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start   date;
    month_end     date;
    part_name     text;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name := 'request_logs_' || to_char(month_start, 'YYYY_MM');

    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF _request_logs FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, month_end
        );
        EXECUTE format(
            'CREATE INDEX idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
            part_name, part_name
        );
        -- 2026-06-24 (migration 043): GIN trgm on client_model so the
        -- /api/logs ?model= ILIKE filter can use a bitmap index scan
        -- instead of a partition Seq Scan once volume grows.
        EXECUTE format(
            'CREATE INDEX idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)',
            part_name, part_name
        );
    END IF;
END;
$$;;

-- ── 分区管理函数：ensure_request_logs_bodies_partition_timestamp_with_time_zone.sql ────────────────────────────────────────────────
--
-- Name: ensure_request_logs_partition(timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.ensure_request_logs_bodies_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start    date;
    month_end      date;
    partition_name text;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name := 'request_logs_bodies_' || to_char(month_start, 'YYYY_MM');

    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        -- 2026-08-23 (migration 562): switched from columnar to heap. The
        -- body columns (request_body / outbound_body / response_body jsonb)
        -- are TOAST-heavy (~350 KB avg) and the hot→monthly promote path
        -- issues INSERT-then-DELETE-when-retried cycles; columnar blocks
        -- UPDATE/DELETE so the bodies pipeline silently stalled, leaving
        -- _request_logs_bodies_hot unbounded. _request_logs_archive remains
        -- columnar (it's a read-only tiered store, see archive_request_logs).
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF _request_logs_bodies
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_request_logs_bodies_partition: created % as heap', partition_name;
    END IF;
END;
$$;;

-- ── 分区管理函数：create_next_month_partitions.sql ────────────────────────────────────────────────
--
-- Name: create_next_month_partitions(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.create_next_month_partitions() RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE
    next_month_start date;
    next_month_end date;
    month_suffix text;
    result text := '';
BEGIN
    next_month_start := date_trunc('month', now() + interval '1 month');
    next_month_end := date_trunc('month', now() + interval '2 months');
    month_suffix := to_char(next_month_start, 'YYYY_MM');
    
    -- usage_ledger
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS usage_ledger_%s
        PARTITION OF usage_ledger
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    result := result || 'usage_ledger_' || month_suffix || ', ';
    
    -- credit_ledger
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS credit_ledger_%s
        PARTITION OF credit_ledger
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    result := result || 'credit_ledger_' || month_suffix || ', ';
    
    -- tool_usage_stats
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS tool_usage_stats_%s
        PARTITION OF tool_usage_stats
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    result := result || 'tool_usage_stats_' || month_suffix || ', ';
    
    -- _request_logs
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS request_logs_%s
        PARTITION OF _request_logs
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    result := result || 'request_logs_' || month_suffix;
    
    RETURN '✅ Created partitions for ' || month_suffix || ': ' || result;
END;
$$;


--
-- Name: FUNCTION create_next_month_partitions(); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.create_next_month_partitions() IS '自动创建下个月的所有 telemetry 表分区（heap 存储）。每月28日执行。';;

-- ── 分区管理函数：ensure_next_month_archive_partition.sql ────────────────────────────────────────────────
--
-- Name: ensure_next_month_archive_partition(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.ensure_next_month_archive_partition() RETURNS void
    LANGUAGE plpgsql
    AS $$
		DECLARE
		    next_month_start date;
		    next_month_end   date;
		    partition_name   text;
		BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    next_month_start := date_trunc('month', now() + interval '1 month')::date;
    next_month_end := date_trunc('month', now() + interval '2 months')::date;
    partition_name := 'request_logs_archive_' || to_char(next_month_start, 'YYYY_MM');

		    IF NOT EXISTS (SELECT 1 FROM pg_class
		                   WHERE relname = partition_name AND relnamespace = 'public'::regnamespace) THEN
		        EXECUTE format(
		            'CREATE TABLE %I PARTITION OF _request_logs_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
		            partition_name, next_month_start, next_month_end
		        );
		    END IF;
		END;
		$$;


--
-- Name: FUNCTION ensure_next_month_archive_partition(); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.ensure_next_month_archive_partition() IS 'Pre-create the next month columnar partition for _request_logs_archive. Call this from a cron job or background service at month-end so the columnar partition is ready when archive_request_logs() is called.';;

COMMIT;
