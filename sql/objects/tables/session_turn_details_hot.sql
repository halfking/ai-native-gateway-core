--
-- Name: session_turn_details_hot; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/733_session_turn_details.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-10-20 补录（审计 D3c）：本表此前在正典目录里没有任何条目，
--   导致「按正典文件读会话族」的人读出一张不存在的表。

CREATE TABLE public.session_turn_details_hot (
    id bigint DEFAULT nextval('public.session_turn_details_id_seq') NOT NULL,
    session_id text NOT NULL,
    turn_no integer NOT NULL,
    tenant_id character varying(255) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    partition_date date DEFAULT CURRENT_DATE NOT NULL,
    client_model text,
    provider_id bigint,
    client_profile text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    transform_rule_id text,
    gw_task_id text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    auto_profile text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    compression_reason text,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[],
    quality_fix_actions jsonb,
    quality_score numeric(3,2),
    stream_chunk_errors integer,
    stream_chunks_sent integer,
    attachments jsonb,
    request_type text,
    request_class text,
    due_at timestamptz,
    node_switch_count integer DEFAULT 0,
    timeout_mode character varying(50),
    effective_timeout_seconds integer,
    is_continuation boolean DEFAULT false,
    cached_response_id bigint,
    context_size_tokens integer,
    keepalive_sent_count integer DEFAULT 0,
    cache_hit boolean,
    cache_tokens_saved integer,
    reasoning_tokens integer,
    image_tokens integer,
    audio_tokens integer,
    video_tokens integer,
    provider_tokens integer,
    protocol_conversion boolean,
    rate_limit_status character varying(50),
    content_safety_score jsonb,
    dlp_violations jsonb,
    sensitive_keywords text[],
    upstream_endpoint text,
    task_id character varying(255),
    task_title text,
    api_key_fingerprint character varying(16),
    provider_model text,
    CONSTRAINT session_turn_details_hot_pkey PRIMARY KEY (id),
    CONSTRAINT session_turn_details_hot_request_key UNIQUE (tenant_id, request_id, partition_date),
    CONSTRAINT session_turn_details_hot_session_turn_key UNIQUE (session_id, turn_no, partition_date)
)
WITH (autovacuum_enabled='true', autovacuum_vacuum_scale_factor='0.05',
      autovacuum_vacuum_threshold='10', autovacuum_analyze_scale_factor='0.02',
      autovacuum_analyze_threshold='50');


--
-- Name: TABLE session_turn_details_hot; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_turn_details_hot IS 'session_turn_details 的 hot 层：writer 只写这里，promote 冷却后搬月分区。';
