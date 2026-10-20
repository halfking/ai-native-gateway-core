-- ===========================================================================
-- File:          sql/migrations/startup/890_drop_request_logs_v1_family.down.sql
-- Migration:     890 DOWN
-- Purpose:       把 v1 `request_logs` 族**骨架**装回去（表 + 序列 + 视图）。
--
-- ⚠⚠⚠ 这不是「零成本回滚」，请先读完再决定：
--
--   1. **数据不可恢复。** 890 删掉的是 v1 族里的**行**；本 down 只能重建
--      **结构**（空表 + 视图），救不回任何一行。要救数据，唯一路径是
--      **在 890 之前**把 v1 族归档到别处——这正是 890 的非空闸存在的理由：
--      族非空时它直接拒绝执行。
--
--   2. 恢复的是**改名后的那一代**（`_request_logs*`）。890 只会跑在 860
--      之后 ⇒ 回滚目标永远是 `_request_logs*` 代际 ⇒ 名字写死，不依赖投递
--      通道的词法改写（改写只认 `request_logs` 整词，`_request_logs` 不动）。
--
--   3. **分区不在本 down 的范围内。** 恢复出来的是母表（无分区）。分区由
--      分区管理任务在下一轮补齐——那些函数**也**被 890 删了，所以本 down
--      一并把它们装回去（见文件末尾）。
--
--   3b. ★ **函数 / 过程 / 触发器全量复原。** 890 up 删掉的是 **20 个**
--      函数/过程 + 2 个触发器函数；本 down 把它们**全部**按 890 执行前的
--      **真库定义**逐字装回去（见文件末尾「v1 函数 / 过程 / 触发器复原」节）。
--      为什么要全量：回滚通常是「代码也一起回滚」——旧版
--      `bg/partition_manager.go` 会调用 promote / archive / TTL 这些函数；
--      只恢复表、不恢复函数 = **半活**（表在、分区不产、正文不清理），
--      每 tick 42P01。
--      （2026-10-20 订正：本 down 此前**只恢复了 4 个**函数，且那 4 个的
--      函数体取自陈旧的正典 `sql/objects/functions/`，与真库部署版**实质
--      不同**——真库版多了 advisory lock 串行、默认分区约束处理、legacy
--      shell 重挂等分支 ⇒ 回滚会装回一个更弱的分区函数。）
--
--   3c. ★ **两个「被替换」而不是「被删除」的函数不在本 down 的复原范围**：
--      `recent_success_rate` 与 `system_health_status` —— 890 把它们的**函数体**
--      换成会话族形态（前者进 provider 候选查询的 CROSS JOIN LATERAL，
--      带 v1 腿就是 100% 流量失败；后者 30s 窗口读 `_request_logs_hot` ⇒ 42P01）。
--      本 down **保留会话族形态**，不还原 v1 腿。理由：
--        · 会话族形态**不引用 v1 表** ⇒ 回滚后不会 42P01；
--        · v1 腿形态在 v1 族空表（890 的非空闸保证回滚时必然是空的）时
--          给出的口径与 canonical 三臂视图**不一致** ⇒ 保留会话族形态反而更自洽；
--        · 这两个函数是 890「**替换**」而非「删除」的对象 ⇒ 本 down 的对象集合
--          门（TestMigration890DownRestoresEveryDroppedFunction）管的是
--          「up 删掉的 ⇒ down 必须复原」，这条不适用（见门里的集合比对）。
--      ⇒ 下次再看到「down 没有还原这两个函数」，那是**有意为之**，不是漏项。
--
--   4. canonical 视图按 **890 执行前的真库定义**逐字还原（三臂体，含
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

-- ── canonical 视图：**三臂体**（取自 890 执行前的真库定义，逐字还原）──────
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

-- ── v1 owner 过滤策略复原（890 的 2c 步把它们的 owner 来源换掉了）──────
--
-- ★ 对称复原：890 的 2c 步把 `session_turns` / `session_turns_hot` 上的两条
--   RESTRICTIVE `*_owner_filter` 策略从「读 v1 族」改成「读 sessions.owner_user」
--   （因为 v1 族一删，策略表达式里的 v1 引用就会 42P01，且 PostgreSQL 会
--   直接拒绝 DROP TABLE）。⇒ 回滚要把它们**还原成 v1 形态**，否则回滚后的
--   库与回滚前的库**不等价**。
--
-- ⚠ 只在「当前策略是 2c 步改写后的形态」时还原：判据 = 策略存在 **且**
--   它的表达式**不再**引用 v1 族。若它本来就不存在（生产形态：0 条
--   owner_filter 策略），本节是 no-op ⇒ **不会凭空造出**生产从来没有的策略。
--
-- ⚠ 名字与表达式里的表名一律写死 `_request_logs*`（860 之后的物理名）
--   ⇒ 不依赖投递通道的词法改写。
DO $$
DECLARE
  v_expr text;
BEGIN
  -- ── session_turns_owner_filter ──
  SELECT coalesce(pg_get_expr(p.polqual, p.polrelid), '') || ' ' ||
         coalesce(pg_get_expr(p.polwithcheck, p.polrelid), '')
    INTO v_expr
    FROM pg_policy p
      JOIN pg_class c ON c.oid = p.polrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = 'public' AND c.relname = 'session_turns'
     AND p.polname = 'session_turns_owner_filter';
  IF v_expr IS NOT NULL AND strpos(v_expr, '_request_logs') = 0 THEN
    EXECUTE $ddl$
      DROP POLICY IF EXISTS session_turns_owner_filter ON public.session_turns;
      CREATE POLICY session_turns_owner_filter ON public.session_turns
        AS RESTRICTIVE
        FOR ALL
        TO PUBLIC
        USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text) OR (EXISTS ( SELECT 1
   FROM ( SELECT DISTINCT ON (request_sources.gw_session_id, request_sources.tenant_id) request_sources.gw_session_id,
            request_sources.tenant_id,
            request_sources.owner_user
           FROM ( SELECT _request_logs_hot.gw_session_id,
                    _request_logs_hot.tenant_id,
                    _request_logs_hot.owner_user,
                    _request_logs_hot.ts
                   FROM _request_logs_hot
                  WHERE (_request_logs_hot.gw_session_id IS NOT NULL)
                UNION ALL
                 SELECT _request_logs.gw_session_id,
                    _request_logs.tenant_id,
                    _request_logs.owner_user,
                    _request_logs.ts
                   FROM _request_logs
                  WHERE (_request_logs.gw_session_id IS NOT NULL)) request_sources
          ORDER BY request_sources.gw_session_id, request_sources.tenant_id, request_sources.ts) first_rl
  WHERE ((first_rl.gw_session_id = session_turns.session_id) AND (first_rl.tenant_id = (session_turns.tenant_id)::text) AND (first_rl.owner_user = current_setting('app.current_user'::text, true)))))))
        WITH CHECK (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text) OR (EXISTS ( SELECT 1
   FROM ( SELECT DISTINCT ON (request_sources.gw_session_id, request_sources.tenant_id) request_sources.gw_session_id,
            request_sources.tenant_id,
            request_sources.owner_user
           FROM ( SELECT _request_logs_hot.gw_session_id,
                    _request_logs_hot.tenant_id,
                    _request_logs_hot.owner_user,
                    _request_logs_hot.ts
                   FROM _request_logs_hot
                  WHERE (_request_logs_hot.gw_session_id IS NOT NULL)
                UNION ALL
                 SELECT _request_logs.gw_session_id,
                    _request_logs.tenant_id,
                    _request_logs.owner_user,
                    _request_logs.ts
                   FROM _request_logs
                  WHERE (_request_logs.gw_session_id IS NOT NULL)) request_sources
          ORDER BY request_sources.gw_session_id, request_sources.tenant_id, request_sources.ts) first_rl
  WHERE ((first_rl.gw_session_id = session_turns.session_id) AND (first_rl.tenant_id = (session_turns.tenant_id)::text) AND (first_rl.owner_user = current_setting('app.current_user'::text, true)))))));
    $ddl$;
  END IF;
  v_expr := NULL;

  -- ── session_turns_hot_owner_filter ──
  SELECT coalesce(pg_get_expr(p.polqual, p.polrelid), '') || ' ' ||
         coalesce(pg_get_expr(p.polwithcheck, p.polrelid), '')
    INTO v_expr
    FROM pg_policy p
      JOIN pg_class c ON c.oid = p.polrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = 'public' AND c.relname = 'session_turns_hot'
     AND p.polname = 'session_turns_hot_owner_filter';
  IF v_expr IS NOT NULL AND strpos(v_expr, '_request_logs') = 0 THEN
    EXECUTE $ddl$
      DROP POLICY IF EXISTS session_turns_hot_owner_filter ON public.session_turns_hot;
      CREATE POLICY session_turns_hot_owner_filter ON public.session_turns_hot
        AS RESTRICTIVE
        FOR ALL
        TO PUBLIC
        USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text) OR (EXISTS ( SELECT 1
   FROM ( SELECT DISTINCT ON (request_sources.gw_session_id, request_sources.tenant_id) request_sources.gw_session_id,
            request_sources.tenant_id,
            request_sources.owner_user
           FROM ( SELECT _request_logs_hot.gw_session_id,
                    _request_logs_hot.tenant_id,
                    _request_logs_hot.owner_user,
                    _request_logs_hot.ts
                   FROM _request_logs_hot
                  WHERE (_request_logs_hot.gw_session_id IS NOT NULL)
                UNION ALL
                 SELECT _request_logs.gw_session_id,
                    _request_logs.tenant_id,
                    _request_logs.owner_user,
                    _request_logs.ts
                   FROM _request_logs
                  WHERE (_request_logs.gw_session_id IS NOT NULL)) request_sources
          ORDER BY request_sources.gw_session_id, request_sources.tenant_id, request_sources.ts) first_rl
  WHERE ((first_rl.gw_session_id = session_turns_hot.session_id) AND (first_rl.tenant_id = (session_turns_hot.tenant_id)::text) AND (first_rl.owner_user = current_setting('app.current_user'::text, true)))))))
        WITH CHECK (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text) OR (EXISTS ( SELECT 1
   FROM ( SELECT DISTINCT ON (request_sources.gw_session_id, request_sources.tenant_id) request_sources.gw_session_id,
            request_sources.tenant_id,
            request_sources.owner_user
           FROM ( SELECT _request_logs_hot.gw_session_id,
                    _request_logs_hot.tenant_id,
                    _request_logs_hot.owner_user,
                    _request_logs_hot.ts
                   FROM _request_logs_hot
                  WHERE (_request_logs_hot.gw_session_id IS NOT NULL)
                UNION ALL
                 SELECT _request_logs.gw_session_id,
                    _request_logs.tenant_id,
                    _request_logs.owner_user,
                    _request_logs.ts
                   FROM _request_logs
                  WHERE (_request_logs.gw_session_id IS NOT NULL)) request_sources
          ORDER BY request_sources.gw_session_id, request_sources.tenant_id, request_sources.ts) first_rl
  WHERE ((first_rl.gw_session_id = session_turns_hot.session_id) AND (first_rl.tenant_id = (session_turns_hot.tenant_id)::text) AND (first_rl.owner_user = current_setting('app.current_user'::text, true))))))
);
    $ddl$;
  END IF;
  v_expr := NULL;

END $$;

-- ── v1 函数 / 过程 / 触发器复原（890 一并删掉的那些）────────────────────────
--
-- ★ 2026-10-20 订正（本节存在的理由就是这条订正）：
-- 本节此前**只恢复了 4 个函数**（ensure_request_logs_partition /
-- ensure_request_logs_bodies_partition / create_next_month_partitions /
-- ensure_next_month_archive_partition），而 890 up 删掉的是 **20 个**函数/过程
-- + 2 个触发器函数 ⇒ 回滚后 v1 族「表回来了、promote / TTL / 归档函数没回来」
-- = **半活**：表在、分区不产、正文不清理。
-- 而那 4 个已恢复的，函数体还取自**正典 sql/objects/functions/**（陈旧），
-- 与**真库部署版实质不同**（真库版多了 advisory lock 串行、默认分区约束处理、
-- legacy shell 重挂等分支）⇒ 回滚会装回一个**更弱**的分区函数。
--
-- ⇒ 本节改为：**20 个函数/过程 + 2 个触发器，全部按 890 执行前的真库定义
--    逐字复原**（来源：pg_get_functiondef / pg_get_triggerdef / obj_description，
--    2026-10-20 于本地真库采样），顺序与 890 up 的 DROP 顺序一致。
--
-- 名字一律写死 `_request_logs*`（860 之后的物理名）⇒ 不依赖投递通道的
-- 词法改写（改写只认 `request_logs` 整词，`_request_logs` 不动）。
-- 已核对：本节所有函数体内**不含**裸 `request_logs` 标识符 ⇒
-- 两条投递通道（runtime 词法改写 / installer 原样）装出来的库完全一致。
--
-- ⚠ 触发器挂在**母表**上（`_request_logs` / `_request_logs_hot`）：
--   分区表上的行级触发器会随 `CREATE TABLE … PARTITION OF` 自动继承
--   ⇒ 后续分区自动带上，不需要逐分区补。

-- ── v1 function：public.ensure_request_logs_partition(target_ts timestamp with time zone) ──
CREATE OR REPLACE FUNCTION public.ensure_request_logs_partition(target_ts timestamp with time zone DEFAULT now())
 RETURNS void
 LANGUAGE plpgsql
AS $function$
DECLARE
    month_start   date;
    month_end     date;
    part_name     text;
    default_oid   regclass;
    was_attached  boolean;
    drained       bigint;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end   := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name   := 'request_logs_' || to_char(month_start, 'YYYY_MM');

    IF EXISTS (
        SELECT 1
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public._request_logs'::regclass
          AND c.relname = part_name
          AND c.relnamespace = 'public'::regnamespace
    ) THEN
        RETURN;
    END IF;

    PERFORM pg_advisory_xact_lock(hashtextextended('llm-gateway:ensure-request-logs-partition', 0));

    -- Re-check under the lock.
    IF EXISTS (
        SELECT 1
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public._request_logs'::regclass
          AND c.relname = part_name
          AND c.relnamespace = 'public'::regnamespace
    ) THEN
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relname = part_name
          AND c.relkind = 'r'
    ) THEN
        -- Legacy detached shell: sync + re-attach. On failure warn and let
        -- inserts keep routing to the default (today's status quo) instead of
        -- failing the caller's promote tick.
        BEGIN
            PERFORM public.sync_partition_columns('public._request_logs'::regclass, part_name::regclass);
            EXECUTE format(
                'ALTER TABLE public._request_logs ATTACH PARTITION public.%I FOR VALUES FROM (%L) TO (%L)',
                part_name, month_start, month_end);
            RAISE NOTICE 'ensure_request_logs_partition: re-attached legacy shell %', part_name;
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'ensure_request_logs_partition: could not re-attach % (%); inserts for this month keep routing to request_logs_default',
                part_name, SQLERRM;
        END;
        RETURN;
    END IF;

    -- Fresh partition. If the default already holds rows for this month
    -- (rows landed before any attached partition existed), CREATE fails with
    -- 23514 ("updated partition constraint for default ... would be
    -- violated"). Self-heal: detach default → create → drain that month →
    -- re-attach. The parent is not under concurrent DDL (we hold the ensure
    -- advisory lock; promote INSERTs for this month route into the new
    -- partition, and out-of-month inserts are practically nonexistent).
    BEGIN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF _request_logs FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, month_end);
    EXCEPTION
        -- 23514 only: the default-constraint violation. Everything else
        -- propagates to the caller (602 contract: loud).
        WHEN check_violation THEN
            SELECT c.oid INTO default_oid
            FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE n.nspname = 'public' AND c.relname = 'request_logs_default' AND c.relkind = 'r';

            IF default_oid IS NULL OR NOT EXISTS (
                SELECT 1 FROM pg_inherits
                WHERE inhrelid = default_oid
                  AND inhparent = 'public._request_logs'::regclass
            ) THEN
                RAISE;  -- not the default-constraint case; rethrow
            END IF;

            EXECUTE 'ALTER TABLE public._request_logs DETACH PARTITION public.request_logs_default';
            EXECUTE format(
                'CREATE TABLE %I PARTITION OF _request_logs FOR VALUES FROM (%L) TO (%L)',
                part_name, month_start, month_end);

            drained := 0;
            LOOP
                WITH batch AS (
                    SELECT ctid
                    FROM public.request_logs_default
                    WHERE ts >= month_start::timestamptz
                      AND ts <  month_end::timestamptz
                    ORDER BY ctid
                    LIMIT 50000
                    FOR UPDATE SKIP LOCKED
                ),
                moved AS (
                    DELETE FROM public.request_logs_default d
                    WHERE d.ctid IN (SELECT ctid FROM batch)
                    RETURNING d.*
                )
                INSERT INTO public._request_logs
                SELECT * FROM moved;

                GET DIAGNOSTICS drained = ROW_COUNT;
                EXIT WHEN drained = 0;
            END LOOP;

            EXECUTE 'ALTER TABLE public._request_logs ATTACH PARTITION public.request_logs_default DEFAULT';
            RAISE NOTICE 'ensure_request_logs_partition: healed default-gap for % (drained rows into new partition)', part_name;
    END;

    EXECUTE format(
        'CREATE INDEX IF NOT EXISTS idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
        part_name, part_name);
    -- 2026-06-24 (migration 043): GIN trgm on client_model so the
    -- /api/logs ?model= ILIKE filter can use a bitmap index scan
    -- instead of a partition Seq Scan once volume grows.
    EXECUTE format(
        'CREATE INDEX IF NOT EXISTS idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)',
        part_name, part_name);
END;
$function$;

-- ── v1 function：public.ensure_request_logs_bodies_partition(target_ts timestamp with time zone) ──
CREATE OR REPLACE FUNCTION public.ensure_request_logs_bodies_partition(target_ts timestamp with time zone DEFAULT now())
 RETURNS void
 LANGUAGE plpgsql
AS $function$
DECLARE
    month_start    date;
    month_end      date;
    partition_name text;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end   := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name := 'request_logs_bodies_' || to_char(month_start, 'YYYY_MM');
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF _request_logs_bodies
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end);
        RAISE NOTICE 'ensure_request_logs_bodies_partition: created % as heap', partition_name;
    END IF;
END;
$function$;

COMMENT ON FUNCTION public.ensure_request_logs_bodies_partition(target_ts timestamp with time zone) IS '创建 _request_logs_bodies 月分区，**恒用 heap**。829 起去掉了 columnar 分支：列存关系一旦出现在未命名子查询里参与 UNION ALL，计划期就抛 invalid perminfoindex 0 in RTE with relid 0（审计 §9.197）。';

-- ── v1 function：public.promote_request_logs_hot_to_partition(p_retention interval, p_batch_size integer) ──
CREATE OR REPLACE FUNCTION public.promote_request_logs_hot_to_partition(p_retention interval DEFAULT '08:00:00'::interval, p_batch_size integer DEFAULT 5000)
 RETURNS bigint
 LANGUAGE plpgsql
AS $function$
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
        FROM public._request_logs_hot
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
         WHERE p.relname = '_request_logs'
           AND am.amname = 'heap'
         ORDER BY 1
    LOOP
        EXECUTE format(
            'UPDATE public._request_logs_hot h
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
    -- from _request_logs_hot BEFORE a separately-protected
    -- INSERT INTO _request_logs SELECT * FROM _promote_hot_batch. Schema drift
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
        FROM public._request_logs_hot
        WHERE ts < statement_timestamp() - p_retention
        ORDER BY ts, id
        LIMIT p_batch_size
        FOR UPDATE SKIP LOCKED
    ),
    moved_rows AS (
        DELETE FROM public._request_logs_hot
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
                system_fingerprint, credits_rate_multiplier
    )
    INSERT INTO public._request_logs (
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
                system_fingerprint, credits_rate_multiplier
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
                system_fingerprint, credits_rate_multiplier
    FROM moved_rows;

    GET DIAGNOSTICS moved = ROW_COUNT;
    RETURN moved;
END;
$function$;

-- ── v1 function：public.promote_request_logs_bodies_hot_to_partition(p_retention interval, p_batch_size integer) ──
CREATE OR REPLACE FUNCTION public.promote_request_logs_bodies_hot_to_partition(p_retention interval DEFAULT '24:00:00'::interval, p_batch_size integer DEFAULT 5000)
 RETURNS bigint
 LANGUAGE plpgsql
AS $function$
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
      FROM public._request_logs_bodies_hot
     WHERE ts < now() - make_interval(days => v_ttl_days)
       AND ts < now() - p_retention
     ORDER BY ts
     LIMIT p_batch_size
  ),
  expired_deleted AS (
    DELETE FROM public._request_logs_bodies_hot
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
    FROM public._request_logs_bodies_hot
    WHERE ts < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_request_logs_bodies_partition(month_rec.month_start);
  END LOOP;

  WITH batch AS (
    SELECT request_id FROM public._request_logs_bodies_hot
    WHERE ts < now() - p_retention
    ORDER BY ts, request_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public._request_logs_bodies_hot h USING batch b
    WHERE h.request_id = b.request_id
    RETURNING h.request_id, h.ts, h.request_body, h.outbound_body, h.response_body
  ), inserted AS (
    INSERT INTO public._request_logs_bodies (
      request_id, ts, request_body, outbound_body, response_body)
    SELECT request_id, ts, request_body, outbound_body, response_body
    FROM moved_rows
    RETURNING request_id
  ) SELECT count(*) INTO v_processed FROM inserted;
  RETURN v_processed;
END;
$function$;

-- ── v1 function：public.promote_request_logs_default_batch(p_retention interval, p_batch_size integer) ──
CREATE OR REPLACE FUNCTION public.promote_request_logs_default_batch(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000)
 RETURNS bigint
 LANGUAGE plpgsql
AS $function$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_rl_batch ON COMMIT DROP AS
    SELECT * FROM public.request_logs_default
    WHERE ts < now() - p_retention
    ORDER BY ts
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.request_logs_default
    WHERE id IN (SELECT id FROM _promote_rl_batch);
    
    BEGIN
        INSERT INTO public._request_logs
        SELECT * FROM _promote_rl_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_request_logs_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$function$;

-- ── v1 function：public.promote_request_logs_bodies_default_batch(p_retention interval, p_batch_size integer) ──
CREATE OR REPLACE FUNCTION public.promote_request_logs_bodies_default_batch(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000)
 RETURNS bigint
 LANGUAGE plpgsql
AS $function$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_rlb_batch ON COMMIT DROP AS
    SELECT * FROM public.request_logs_bodies_default
    WHERE ts < now() - p_retention
    ORDER BY ts
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.request_logs_bodies_default
    WHERE request_id IN (SELECT request_id FROM _promote_rlb_batch)
      AND ts IN (SELECT ts FROM _promote_rlb_batch);
    
    BEGIN
        INSERT INTO public._request_logs_bodies
        SELECT * FROM _promote_rlb_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_request_logs_bodies_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$function$;

-- ── v1 function：public.create_next_month_partitions() ──
CREATE OR REPLACE FUNCTION public.create_next_month_partitions()
 RETURNS text
 LANGUAGE plpgsql
AS $function$
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
$function$;

COMMENT ON FUNCTION public.create_next_month_partitions() IS '自动创建下个月的所有 telemetry 表分区（heap 存储）。每月28日执行。';

-- ── v1 function：public.ensure_next_month_archive_partition() ──
CREATE OR REPLACE FUNCTION public.ensure_next_month_archive_partition()
 RETURNS void
 LANGUAGE plpgsql
AS $function$
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
		$function$;

COMMENT ON FUNCTION public.ensure_next_month_archive_partition() IS 'Pre-create the next month columnar partition for request_logs_archive. Call this from a cron job or background service at month-end so the columnar partition is ready when archive_request_logs() is called.';

-- ── v1 function：public.archive_request_logs(archive_month date) ──
CREATE OR REPLACE FUNCTION public.archive_request_logs(archive_month date)
 RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
 LANGUAGE plpgsql
AS $function$
		DECLARE
		    month_start date := date_trunc('month', archive_month)::date;
		    month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
		    src_part    text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
		    dst_part    text := 'request_logs_archive_' || to_char(month_start, 'YYYY_MM');
		    row_count   bigint;
		    col_list    text;
		BEGIN
		    IF NOT EXISTS (SELECT 1 FROM pg_class
		                   WHERE relname = src_part AND relnamespace = 'public'::regnamespace) THEN
		        RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
		        RETURN;
		    END IF;

		    IF NOT EXISTS (SELECT 1 FROM pg_class
		                   WHERE relname = dst_part AND relnamespace = 'public'::regnamespace) THEN
		        EXECUTE format(
		            'CREATE TABLE %I PARTITION OF _request_logs_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
		            dst_part, month_start, month_end
		        );
		    END IF;

		    SELECT string_agg(a.column_name, ', ' ORDER BY a.ordinal_position)
		    INTO col_list
		    FROM information_schema.columns a
		    JOIN information_schema.columns r
		      ON a.table_schema = r.table_schema
		     AND a.column_name  = r.column_name
		    WHERE a.table_name = '_request_logs_archive'
		      AND r.table_name = src_part
		      AND a.table_schema = 'public'
		      AND a.ordinal_position > 0;

		    IF col_list IS NULL OR length(col_list) = 0 THEN
		        RAISE EXCEPTION 'No common columns between % and _request_logs_archive', src_part;
		    END IF;

		    EXECUTE format(
		        'INSERT INTO %I (%s) SELECT %s FROM %I',
		        dst_part, col_list, col_list, src_part
		    );
		    GET DIAGNOSTICS row_count = ROW_COUNT;

		    EXECUTE format('ALTER TABLE _request_logs DETACH PARTITION %I', src_part);
		    EXECUTE format('DROP TABLE %I', src_part);

		    RETURN QUERY SELECT 'success'::text, row_count, true;
		END;
		$function$;

COMMENT ON FUNCTION public.archive_request_logs(archive_month date) IS 'Archive one month of request_logs into request_logs_archive (heap). Column-aware: explicit column list, robust against column-order differences. Chunked INSERT (1000 rows/iter) is a checkpoint granularity — heap has no in-memory buffer cap. request_logs_archive is intentionally NOT columnar: the table carries large JSONB columns (request_body, response_body, outbound_body) that overflow Citus columnar''s 1 GB string buffer on serialization. See migration 318b for the full rationale and the future body-table split plan. Idempotent in practice because the source partition is dropped after a successful run.';

-- ── v1 function：public.archive_request_logs_default(p_retention_days integer) ──
CREATE OR REPLACE FUNCTION public.archive_request_logs_default(p_retention_days integer)
 RETURNS TABLE(archived_partition text, rows_archived bigint)
 LANGUAGE plpgsql
AS $function$
DECLARE
    batch_size CONSTANT integer := 1000;
    cutoff_ts  timestamptz;
    rec        record;
    src_part   text;
    dst_part   text;
    month_start date;
    month_end   date;
    new_last_id  bigint;
    batch_count  bigint;
    last_id      bigint;
    total_rows   bigint;
BEGIN
    -- 守卫: 与 settings/spec_lifecycle.go Min/Max=7/365 保持一致；留存
    -- 边界是 owner 拍板（业务对账窗口 ≈ 7 天 = 法定合规最低线）。
    IF p_retention_days < 7 THEN
        RAISE EXCEPTION 'archive_request_logs_default: retention_days=% < 7 floor', p_retention_days;
    END IF;
    IF p_retention_days > 365 THEN
        RAISE EXCEPTION 'archive_request_logs_default: retention_days=% > 365 ceiling', p_retention_days;
    END IF;

    cutoff_ts := NOW() - make_interval(days => p_retention_days);

    -- 月分区游走: pg_inherits 枚举 _request_logs 下形如
    -- request_logs_YYYY_MM 的月分区，剔除 DEFAULT / bodies 同族
    -- (_bodies_2026_07 等不会被 pg_inherits 列入 _request_logs 子集，
    -- 此处命名正则 ^request_logs_[0-9]{4}_[0-9]{2}$ 即足够严密；
    -- 十六轮审计补钉 relnamespace=public——其他 schema 下的同名
    -- 父表子分区不会被误收，与下方归档表存在性守卫同款口径)。
    FOR rec IN
        SELECT
            c.relname                                    AS partition_name,
            to_date(
                substring(c.relname FROM 'request_logs_([0-9]{4}_[0-9]{2})$'),
                'YYYY_MM'
            )                                            AS partition_month
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = '_request_logs'
          AND p.relnamespace = 'public'::regnamespace
          AND c.relname ~ '^request_logs_[0-9]{4}_[0-9]{2}$'
        ORDER BY partition_month
    LOOP
        month_start := date_trunc('month', rec.partition_month)::date;
        month_end   := (month_start + INTERVAL '1 month')::date;

        -- 跳过仍在保留窗口内的月分区。
        IF month_end > cutoff_ts THEN
            CONTINUE;
        END IF;

        src_part := rec.partition_name;
        dst_part := 'request_logs_archive_' || to_char(month_start, 'YYYY_MM');

        -- 串行化同一月份的并发 ensure（多实例 partition_manager、
        -- 共享库 boot ensure、installer 升级通道同时投递时不会双跑）。
        PERFORM pg_advisory_xact_lock(
            hashtext('archive_request_logs_default:' || to_char(month_start, 'YYYY-MM'))
        );

        -- 归档表: 独立 heap 表（非 columnar——归档按月分片后读取
        -- 通常是单分区时间窗扫描，columnar 收益不明显；非分区父
        -- 表——挂载到 _request_logs_archive 会让 archive_default
        -- 与具体分区并存，迁移阶段更难治理）。11 个摘要字段+
        -- UNIQUE(request_id, ts) 兜住 ON CONFLICT DO NOTHING
        -- 幂等需求；二级索引 (ts) 兜对账窗口扫表。
        IF NOT EXISTS (
            SELECT 1 FROM pg_class
            WHERE relname = dst_part AND relnamespace = 'public'::regnamespace
        ) THEN
            EXECUTE format(
                'CREATE TABLE %I (
                    request_id        text             NOT NULL,
                    ts                timestamptz      NOT NULL,
                    tenant_id         text,
                    session_id        text,
                    model             text,
                    prompt_tokens     integer,
                    completion_tokens integer,
                    cost_usd          numeric(14, 8),
                    status_code       integer,
                    success           boolean,
                    error_kind        text,
                    archived_at       timestamptz      NOT NULL DEFAULT NOW()
                )', dst_part);
            EXECUTE format(
                'CREATE UNIQUE INDEX %I ON %I (request_id, ts)',
                dst_part || '_req_ts_uniq', dst_part);
            EXECUTE format(
                'CREATE INDEX %I ON %I (ts DESC)',
                dst_part || '_ts_idx', dst_part);
        END IF;

        -- 主键游标批式 INSERT: 单分区内 id bigint 唯一（PK 是
        -- (id, ts) 但 ts 在月内被约束到同一月，id 局部唯一）。
        -- ON CONFLICT (request_id, ts) DO NOTHING 让重跑同月分区
        -- 时已存在的行被吸收，rows_archived 只计新增。
        total_rows := 0;
        last_id    := 0;
        LOOP
            -- 单查询三件事: 取下一批候选 → 写归档 → 返回 new_last_id
            -- 与本批实际写入数。CTE 顺序: candidates 取源、max_id
            -- 算下一轮游标、inserted 写归档并返回被插入行数。
            EXECUTE format(
                'WITH
                 candidates AS (
                     SELECT id, request_id, ts, tenant_id, gw_session_id,
                            provider_model, prompt_tokens, completion_tokens,
                            cost_usd, upstream_status_code, success, error_kind
                     FROM %I
                     WHERE id > %L
                     ORDER BY id
                     LIMIT %L
                 ),
                 max_id AS (
                     SELECT COALESCE(MAX(id), 0) AS new_last FROM candidates
                 ),
                 inserted AS (
                     INSERT INTO %I (
                         request_id, ts, tenant_id, session_id, model,
                         prompt_tokens, completion_tokens, cost_usd,
                         status_code, success, error_kind
                     )
                     SELECT request_id, ts, tenant_id, gw_session_id,
                            provider_model, prompt_tokens, completion_tokens,
                            cost_usd, upstream_status_code, success, error_kind
                     FROM candidates
                     ON CONFLICT (request_id, ts) DO NOTHING
                     RETURNING 1 AS r
                 )
                 SELECT
                     (SELECT new_last FROM max_id),
                     (SELECT count(*) FROM inserted)',
                src_part, last_id, batch_size,
                dst_part
            ) INTO new_last_id, batch_count;

            total_rows := total_rows + batch_count;

            -- 终止条件: 无候选（new_last_id = 0）或已读完该分区
            -- （本批不足 batch_size 行）。前者兜极端空源，后者兜
            -- 末批。R73 订正：两条 EXIT 是「任一成立即退出」，且
            -- 第二条在空源时同样命中（0 - last_id < batch_size），
            -- 第一条实为冗余保险——任一即退正是防全冲突批次死循环
            -- 的机制（冲突不减少候选数，id 游标只增，循环必然收敛）。
            EXIT WHEN new_last_id = 0;
            EXIT WHEN (new_last_id - last_id) < batch_size;

            last_id := new_last_id;
        END LOOP;

        archived_partition := dst_part;
        rows_archived      := total_rows;
        RETURN NEXT;
    END LOOP;
END;
$function$;

COMMENT ON FUNCTION public.archive_request_logs_default(p_retention_days integer) IS 'Archive summary fields from _request_logs monthly partitions older than p_retention_days (7-365) into per-month heap tables request_logs_archive_YYYY_MM. Idempotent (ON CONFLICT DO NOTHING), batched at 1000 rows/iteration via id cursor, advisory-lock-serialized per month. Does NOT drop source partition (R68 freeze). Migration 754 (2026-09-26). Refs: docs/audit/2026-09-25-session-storage-audit-handoff.md §9.';

-- ── v1 function：public.repair_request_logs_detached_partitions() ──
CREATE OR REPLACE FUNCTION public.repair_request_logs_detached_partitions()
 RETURNS bigint
 LANGUAGE plpgsql
AS $function$
DECLARE
    parent_oid   regclass := 'public._request_logs'::regclass;
    default_name text     := 'request_logs_default';
    default_oid  regclass;
    was_attached boolean := false;
    shell        record;
    month_start  date;
    month_end    date;
    drained      bigint;
    total        bigint := 0;
BEGIN
    -- 687/694 convention: partition bounds and the ::timestamptz drain
    -- predicate both read the session TimeZone — pin Shanghai so a UTC
    -- session cannot create 08:00+08 bounds.
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    PERFORM pg_advisory_xact_lock(hashtextextended('llm-gateway:repair-request-logs-detached', 0));

    SELECT c.oid INTO default_oid
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND c.relname = default_name AND c.relkind = 'r';

    -- Step 1: detach the DEFAULT partition so shell ATTACHes skip the
    -- default-constraint validation scan entirely. The txn holds ACCESS
    -- EXCLUSIVE on the parent until COMMIT, so concurrent promote/tick
    -- statements queue rather than error into the no-default window.
    IF default_oid IS NOT NULL THEN
        was_attached := EXISTS (
            SELECT 1 FROM pg_inherits
            WHERE inhrelid = default_oid
              AND inhparent = parent_oid);
        IF was_attached THEN
            EXECUTE format('ALTER TABLE public._request_logs DETACH PARTITION public.%I', default_name);
        END IF;
    END IF;

    -- Step 2: per standalone shell — sync columns, ATTACH, then drain that
    -- month out of the standalone default through the PARENT (routing, so
    -- column order can never drift positionally).
    FOR shell IN
        SELECT c.relname, c.oid
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relkind = 'r'
          AND c.relname ~ '^request_logs_[0-9]{4}_[0-9]{2}$'
          AND NOT EXISTS (
              SELECT 1 FROM pg_inherits i
              WHERE i.inhrelid = c.oid
                AND i.inhparent = parent_oid)
        ORDER BY c.relname
    LOOP
        month_start := to_date(substring(shell.relname from '[0-9]{4}_[0-9]{2}$'), 'YYYY_MM');
        month_end   := (date_trunc('month', month_start::timestamp) + interval '1 month')::date;

        BEGIN
            PERFORM public.sync_partition_columns(parent_oid, shell.oid);
            EXECUTE format(
                'ALTER TABLE public._request_logs ATTACH PARTITION public.%I FOR VALUES FROM (%L) TO (%L)',
                shell.relname, month_start, month_end);
        EXCEPTION WHEN OTHERS THEN
            -- Leave this month in the default; ensure()'s re-attach branch
            -- and a re-run of this repair will retry it. Nothing was drained,
            -- so no row is stranded outside every partition.
            RAISE WARNING 'repair_request_logs_detached_partitions: could not re-attach % (%); month rows remain in request_logs_default',
                shell.relname, SQLERRM;
            CONTINUE;
        END;

        -- Attached: now drain this month from the standalone default. Any
        -- failure here propagates (loud) — the txn rolls back to the safe
        -- pre-repair state rather than half-attaching.
        IF default_oid IS NOT NULL THEN
            LOOP
                WITH batch AS (
                    SELECT ctid
                    FROM public.request_logs_default
                    WHERE ts >= month_start::timestamptz
                      AND ts <  month_end::timestamptz
                    ORDER BY ctid
                    LIMIT 50000
                    FOR UPDATE SKIP LOCKED
                ),
                moved AS (
                    DELETE FROM public.request_logs_default d
                    WHERE d.ctid IN (SELECT ctid FROM batch)
                    RETURNING d.*
                )
                INSERT INTO public._request_logs
                SELECT * FROM moved;

                GET DIAGNOSTICS drained = ROW_COUNT;
                total := total + drained;
                EXIT WHEN drained = 0;
            END LOOP;
        END IF;
        RAISE NOTICE 'repair_request_logs_detached_partitions: attached %', shell.relname;
    END LOOP;

    -- Step 3: put the default back (only if this call detached it — a
    -- default that arrived standalone stays untouched). Remaining rows are
    -- exactly the months without an attached partition, so the overlap
    -- validation passes.
    IF default_oid IS NOT NULL AND was_attached THEN
        EXECUTE format('ALTER TABLE public._request_logs ATTACH PARTITION public.%I DEFAULT', default_name);
    END IF;

    RETURN total;
END;
$function$;

-- ── v1 procedure：public.backfill_request_logs_bodies(IN p_batch integer) ──
CREATE OR REPLACE PROCEDURE public.backfill_request_logs_bodies(IN p_batch integer DEFAULT 200)
 LANGUAGE plpgsql
AS $procedure$
DECLARE
    rec record;
    inserted int := 0;
BEGIN
    FOR rec IN
        SELECT id
        FROM _request_logs
        WHERE (request_body IS NOT NULL OR outbound_body IS NOT NULL OR response_body IS NOT NULL)
          AND NOT EXISTS (
              SELECT 1 FROM _request_logs_bodies b
              WHERE b.request_id = _request_logs.request_id AND b.ts = _request_logs.ts)
        ORDER BY id
        LIMIT p_batch
    LOOP
        INSERT INTO _request_logs_bodies (request_id, ts, request_body, outbound_body, response_body)
        SELECT request_id, ts, request_body, outbound_body, response_body
        FROM _request_logs
        WHERE id = rec.id;
        inserted := inserted + 1;
    END LOOP;

    RAISE NOTICE 'backfill_request_logs_bodies: inserted % rows (batch %)',
        inserted, p_batch;
END;
$procedure$;

-- ── v1 function：public.cleanup_stale_in_progress_requests() ──
CREATE OR REPLACE FUNCTION public.cleanup_stale_in_progress_requests()
 RETURNS TABLE(cleaned_count bigint)
 LANGUAGE plpgsql
AS $function$
DECLARE
    updated_rows bigint;
BEGIN
    UPDATE _request_logs_hot
    SET
        success = false,
        request_status = 'failure',
        error_kind = 'gateway_timeout',
        failure_stage = 'gateway',
        failure_detail_code = 'gw_processing_timeout'
    WHERE
        request_status = 'in_progress'
        AND ts < NOW() - INTERVAL '5 minutes'
        AND success = false;

    GET DIAGNOSTICS updated_rows = ROW_COUNT;

    IF updated_rows > 0 THEN
        RAISE NOTICE 'Cleaned up % stale in_progress _request_logs_hot records', updated_rows;
    END IF;

    RETURN QUERY SELECT updated_rows;
END;
$function$;

-- ── v1 function：public.credential_most_used_model(p_credential_id bigint, p_window_hours integer) ──
CREATE OR REPLACE FUNCTION public.credential_most_used_model(p_credential_id bigint, p_window_hours integer DEFAULT 24)
 RETURNS TABLE(raw_model_name text, call_count bigint)
 LANGUAGE sql
 STABLE
AS $function$
    SELECT pm.raw_model_name, COUNT(*) AS call_count
    FROM _request_logs_hot rl
    JOIN provider_models pm ON pm.id = rl.canonical_id
    WHERE rl.credential_id = p_credential_id
      AND rl.ts >= now() - make_interval(hours => p_window_hours)
      AND rl.success = TRUE
    GROUP BY pm.raw_model_name
    ORDER BY call_count DESC, pm.raw_model_name ASC
    LIMIT 1;
$function$;

COMMENT ON FUNCTION public.credential_most_used_model(p_credential_id bigint, p_window_hours integer) IS '341: top-1 model by 24h successful traffic for a credential. Used by credential_selfcheck to pick the daily probe model.';

-- ── v1 function：public.credential_most_used_model(p_credential_id integer, p_lookback_hours integer) ──
CREATE OR REPLACE FUNCTION public.credential_most_used_model(p_credential_id integer, p_lookback_hours integer DEFAULT 24)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
AS $function$
DECLARE
    v_model TEXT;
BEGIN
    -- 主路径：_request_logs_hot (热表，毫秒级)
    SELECT rl.model
      INTO v_model
      FROM _request_logs_hot rl
     WHERE rl.credential_id = p_credential_id
       AND rl.success = TRUE
       AND rl.started_at >= now() - make_interval(hours => p_lookback_hours)
       AND rl.model IS NOT NULL
     GROUP BY rl.model
     ORDER BY COUNT(*) DESC, rl.model
     LIMIT 1;

    IF v_model IS NOT NULL THEN
        RETURN v_model;
    END IF;

    -- Fallback: 冷表 _request_logs (90 天后迁过去的)
    SELECT rl.model
      INTO v_model
      FROM _request_logs rl
     WHERE rl.credential_id = p_credential_id
       AND rl.success = TRUE
       AND rl.started_at >= now() - make_interval(hours => p_lookback_hours)
       AND rl.model IS NOT NULL
     GROUP BY rl.model
     ORDER BY COUNT(*) DESC, rl.model
     LIMIT 1;

    RETURN v_model;
END;
$function$;

COMMENT ON FUNCTION public.credential_most_used_model(p_credential_id integer, p_lookback_hours integer) IS 'Returns the raw_model_name with the most successful requests for a credential in the last N hours. Used by bg/credential_selfcheck.go to pick a fallback probe model.';

-- ── v1 function：public.get_last_successful_request(p_session_id character varying, p_minutes integer) ──
CREATE OR REPLACE FUNCTION public.get_last_successful_request(p_session_id character varying, p_minutes integer DEFAULT 60)
 RETURNS TABLE(request_id bigint, created_at timestamp with time zone, latency_ms integer, response_text text)
 LANGUAGE plpgsql
AS $function$
BEGIN
    RETURN QUERY
    SELECT 
        id as request_id,
        r.created_at,
        r.latency_ms,
        r.response_text
    FROM _request_logs r
    WHERE r.session_id = p_session_id
      AND r.success = true
      AND r.created_at > NOW() - (p_minutes || ' minutes')::INTERVAL
    ORDER BY r.created_at DESC
    LIMIT 1;
END;
$function$;

COMMENT ON FUNCTION public.get_last_successful_request(p_session_id character varying, p_minutes integer) IS '获取会话最后一次成功请求（用于继续/重试检测）';

-- ── v1 function：public.update_api_key_model_cost() ──
CREATE OR REPLACE FUNCTION public.update_api_key_model_cost()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
DECLARE
    bucket_ts TIMESTAMPTZ;
    key_id INT;
    limit_val INT;
BEGIN
    -- 计算 5min bucket（向下取整）
    bucket_ts := date_trunc('hour', NEW.ts)
                  + (FLOOR(EXTRACT(minute FROM NEW.ts) / 5) * INTERVAL '5 minutes');
    key_id := NEW.api_key_id;
    IF key_id IS NULL THEN
        RETURN NEW;
    END IF;

    -- 查找 api_key 的 rate_limit_rpm（作为该 key 的并发近似上限）
    -- 注意：api_keys 表没有 concurrency_limit 列（已在 realtime-trigger SQL 中确认）。
    -- 用 rate_limit_rpm / 10 作为近似（假设平均请求耗时 6 秒）。
    SELECT COALESCE(rate_limit_rpm, 0) / 10 INTO limit_val
    FROM api_keys WHERE id = key_id;

    -- 增量更新（注意：不在这里累加 active_concurrent，因为 AFTER INSERT 只能加不能减。
    -- active_concurrent 由 customer_cost_view 通过 JOIN _request_logs 实时计算）
    INSERT INTO api_key_model_cost (
        bucket, api_key_id, canonical_id, raw_model, billing_mode,
        requests_total, requests_success,
        tokens_input, tokens_output, cost_usd,
        active_concurrent, concurrency_limit, pressure_ratio,
        last_request_at, updated_at
    ) VALUES (
        bucket_ts, key_id, NEW.canonical_id, COALESCE(NEW.outbound_model, NEW.client_model),
        'token',
        1, CASE WHEN NEW.success THEN 1 ELSE 0 END,
        COALESCE(NEW.prompt_tokens, 0), COALESCE(NEW.completion_tokens, 0),
        COALESCE(NEW.cost_usd, 0),
        1, limit_val,
        CASE WHEN limit_val > 0 THEN LEAST(1.0, 1.0 / limit_val) ELSE 0 END,
        NEW.ts, NOW()
    )
    ON CONFLICT (bucket, api_key_id, raw_model) DO UPDATE SET
        requests_total    = api_key_model_cost.requests_total + 1,
        requests_success  = api_key_model_cost.requests_success + CASE WHEN NEW.success THEN 1 ELSE 0 END,
        tokens_input      = api_key_model_cost.tokens_input + COALESCE(NEW.prompt_tokens, 0),
        tokens_output     = api_key_model_cost.tokens_output + COALESCE(NEW.completion_tokens, 0),
        cost_usd          = api_key_model_cost.cost_usd + COALESCE(NEW.cost_usd, 0),
        -- active_concurrent 在 trigger 中不更新（只在视图层动态计算）
        concurrency_limit = EXCLUDED.concurrency_limit,
        pressure_ratio    = CASE WHEN EXCLUDED.concurrency_limit > 0
                                  THEN LEAST(1.0, EXCLUDED.active_concurrent::numeric / EXCLUDED.concurrency_limit)
                                  ELSE 0 END,
        last_request_at   = NEW.ts,
        updated_at        = NOW();

    RETURN NEW;
END;
$function$;

-- ── v1 function：public.drop_old_request_logs_bodies_partitions(p_retention_days integer) ──
CREATE OR REPLACE FUNCTION public.drop_old_request_logs_bodies_partitions(p_retention_days integer)
 RETURNS TABLE(dropped_partition text, rows_dropped bigint)
 LANGUAGE plpgsql
AS $function$
DECLARE
    cutoff_date date := CURRENT_DATE - p_retention_days;
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT
            c.relname AS partition_name,
            pg_total_relation_size(c.oid) AS size_bytes
        FROM pg_class c
        JOIN pg_inherits i ON i.inhrelid = c.oid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = '_request_logs_bodies'
          AND c.relname ~ '^request_logs_bodies_[0-9]{4}_[0-9]{2}$'
    LOOP
        DECLARE
            partition_month date := to_date(
                substring(rec.partition_name FROM 'request_logs_bodies_([0-9]{4}_[0-9]{2})$'),
                'YYYY_MM'
            );
            month_end date := partition_month + interval '1 month';
        BEGIN
            IF month_end <= cutoff_date THEN
                EXECUTE format('DROP TABLE %I', rec.partition_name);
                RAISE NOTICE 'drop_old_request_logs_bodies_partitions: dropped %', rec.partition_name;
                dropped_partition := rec.partition_name;
                rows_dropped := -1;
                RETURN NEXT;
            END IF;
        END;
    END LOOP;
END;
$function$;

-- ── v1 function：public.update_session_tenant_binding() ──
CREATE OR REPLACE FUNCTION public.update_session_tenant_binding()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
    -- Only process if both gw_session_id and tenant_id are present
    IF NEW.gw_session_id IS NOT NULL AND NEW.tenant_id IS NOT NULL THEN
        INSERT INTO session_tenant_binding (
            gw_session_id, 
            tenant_id, 
            first_seen_at, 
            last_seen_at, 
            request_count
        )
        VALUES (
            NEW.gw_session_id, 
            NEW.tenant_id, 
            NEW.ts, 
            NEW.ts, 
            1
        )
        ON CONFLICT (gw_session_id) DO UPDATE SET
            last_seen_at = EXCLUDED.last_seen_at,
            request_count = session_tenant_binding.request_count + 1,
            -- CRITICAL: Detect tenant switch (cross-talk)
            tenant_id = CASE
                WHEN session_tenant_binding.tenant_id != EXCLUDED.tenant_id 
                     AND NOT session_tenant_binding.tenant_id LIKE 'CONFLICT:%'
                THEN 'CONFLICT:' || session_tenant_binding.tenant_id || ',' || EXCLUDED.tenant_id
                WHEN session_tenant_binding.tenant_id LIKE 'CONFLICT:%'
                     AND session_tenant_binding.tenant_id NOT LIKE '%' || EXCLUDED.tenant_id || '%'
                THEN session_tenant_binding.tenant_id || ',' || EXCLUDED.tenant_id
                ELSE session_tenant_binding.tenant_id
            END;
    END IF;
    RETURN NEW;
END;
$function$;

COMMENT ON FUNCTION public.update_session_tenant_binding() IS 'Auto-updates session_tenant_binding on _request_logs INSERT. Detects cross-tenant session access.';

-- ── v1 function：public.update_session_summary() ──
CREATE OR REPLACE FUNCTION public.update_session_summary()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
DECLARE
    v_prompt_tokens     BIGINT;
    v_completion_tokens BIGINT;
    v_total_tokens      BIGINT;
    v_cost              DECIMAL(14,8);
    v_input_cost        DECIMAL(12,6);
    v_output_cost       DECIMAL(12,6);
    v_latency_ms        INT;
    v_is_success        BOOLEAN;
    v_client_model      VARCHAR(100);
    v_upstream_model    VARCHAR(100);
    v_work_type         VARCHAR(50);
    v_provider_code     VARCHAR(100);
    v_gw_session_id     VARCHAR(128);
    v_token_ratio       DECIMAL(10,6);
BEGIN
    v_gw_session_id := NEW.gw_session_id;
    IF v_gw_session_id IS NULL OR v_gw_session_id = '' THEN
        RETURN NEW;
    END IF;

    v_prompt_tokens     := COALESCE(NEW.prompt_tokens, 0);
    v_completion_tokens := COALESCE(NEW.completion_tokens, 0);
    v_total_tokens      := v_prompt_tokens + v_completion_tokens;
    v_cost              := COALESCE(NEW.cost_usd, 0);
    v_latency_ms        := COALESCE(NEW.latency_ms, 0);
    v_is_success        := COALESCE(NEW.success, false);
    v_client_model      := NEW.client_model;
    v_upstream_model    := NEW.outbound_model;
    v_work_type         := NEW.work_type;

    IF v_total_tokens > 0 THEN
        -- 无界 numeric 相除（比值 ∈ (0,1]）；572 修复体，661 重申。
        v_token_ratio := v_prompt_tokens::numeric / v_total_tokens::numeric;
    ELSE
        v_token_ratio := 0.5;
    END IF;
    v_input_cost  := (v_cost * v_token_ratio)::DECIMAL(12,6);
    v_output_cost := (v_cost - v_input_cost)::DECIMAL(12,6);

    BEGIN
        SELECT p.code INTO v_provider_code
        FROM providers p
        WHERE p.id = NEW.provider_id
        LIMIT 1;
    EXCEPTION WHEN OTHERS THEN
        v_provider_code := NULL;
    END;

    INSERT INTO session_summaries (
        session_key, tenant_id, first_request_at, last_request_at,
        request_count, success_count, error_count,
        total_cost_usd, input_cost_usd, output_cost_usd,
        total_prompt_tokens, total_completion_tokens,
        avg_latency_ms, min_latency_ms, max_latency_ms,
        models_used, work_types, providers, client_models, updated_at
    ) VALUES (
        v_gw_session_id, NEW.tenant_id, NEW.ts, NEW.ts,
        1, CASE WHEN v_is_success THEN 1 ELSE 0 END,
        CASE WHEN v_is_success THEN 0 ELSE 1 END,
        v_cost, v_input_cost, v_output_cost,
        v_prompt_tokens, v_completion_tokens,
        v_latency_ms, v_latency_ms, v_latency_ms,
        CASE WHEN v_upstream_model IS NOT NULL THEN ARRAY[v_upstream_model]::TEXT[] ELSE '{}'::TEXT[] END,
        CASE WHEN v_work_type IS NOT NULL THEN ARRAY[v_work_type]::TEXT[] ELSE '{}'::TEXT[] END,
        CASE WHEN v_provider_code IS NOT NULL THEN ARRAY[v_provider_code]::TEXT[] ELSE '{}'::TEXT[] END,
        CASE WHEN v_client_model IS NOT NULL THEN ARRAY[v_client_model]::TEXT[] ELSE '{}'::TEXT[] END,
        NOW()
    ) ON CONFLICT (session_key) DO UPDATE SET
        last_request_at = GREATEST(session_summaries.last_request_at, NEW.ts),
        request_count = session_summaries.request_count + 1,
        success_count = session_summaries.success_count + CASE WHEN v_is_success THEN 1 ELSE 0 END,
        error_count = session_summaries.error_count + CASE WHEN v_is_success THEN 0 ELSE 1 END,
        total_cost_usd = session_summaries.total_cost_usd + v_cost,
        input_cost_usd = session_summaries.input_cost_usd + v_input_cost,
        output_cost_usd = session_summaries.output_cost_usd + v_output_cost,
        total_prompt_tokens = session_summaries.total_prompt_tokens + v_prompt_tokens,
        total_completion_tokens = session_summaries.total_completion_tokens + v_completion_tokens,
        avg_latency_ms = ((session_summaries.avg_latency_ms * session_summaries.request_count + v_latency_ms)
            / (session_summaries.request_count + 1))::INT,
        min_latency_ms = LEAST(session_summaries.min_latency_ms, v_latency_ms),
        max_latency_ms = GREATEST(session_summaries.max_latency_ms, v_latency_ms),
        models_used = array_unique_append(session_summaries.models_used, v_upstream_model),
        work_types = array_unique_append(session_summaries.work_types, v_work_type),
        providers = array_unique_append(session_summaries.providers, v_provider_code),
        client_models = array_unique_append(session_summaries.client_models, v_client_model),
        primary_model = COALESCE(session_summaries.primary_model, v_upstream_model),
        updated_at = NOW();

    RETURN NEW;
END;
$function$;

-- ── v1 触发器（随表消失，触发器函数本身由上面两条复原）──────────────
DROP TRIGGER IF EXISTS trg_session_tenant_binding ON public._request_logs;
CREATE TRIGGER trg_session_tenant_binding AFTER INSERT ON public._request_logs
    FOR EACH ROW EXECUTE FUNCTION update_session_tenant_binding();

DROP TRIGGER IF EXISTS trg_update_session_summary ON public._request_logs_hot;
CREATE TRIGGER trg_update_session_summary AFTER INSERT ON public._request_logs_hot
    FOR EACH ROW WHEN (((new.gw_session_id IS NOT NULL) AND (new.gw_session_id <> ''::text)))
    EXECUTE FUNCTION update_session_summary();

COMMIT;
