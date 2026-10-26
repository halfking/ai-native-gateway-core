--
-- Name: session_turns_hot; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/526_session_turns_hot.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-10-20 补录（审计 D3c）：本表此前在正典目录里没有任何条目，
--   导致「按正典文件读会话族」的人读出一张不存在的表。

CREATE TABLE public.session_turns_hot (
    id BIGINT NOT NULL DEFAULT nextval('public.session_turns_id_seq'::regclass),
    session_id TEXT NOT NULL,
    turn_no INTEGER NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    request_id TEXT NOT NULL,
    project_id TEXT,
    namespace TEXT,
    parent_request_id TEXT,
    task_type TEXT,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    submit_mode TEXT NOT NULL DEFAULT 'full',
    compression_applied BOOLEAN DEFAULT FALSE,
    compression_strategy TEXT,
    compression_meta JSONB DEFAULT '{}'::JSONB,
    compression_tokens_saved INTEGER,
    injection_verdict TEXT DEFAULT 'skip',
    output_verdict TEXT DEFAULT 'skip',
    model TEXT,
    provider TEXT,
    credential_id TEXT,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    cache_read_tokens INTEGER,
    cache_write_tokens INTEGER,
    cost_usd NUMERIC(12,6),
    latency_ms INTEGER,
    status_code INTEGER,
    success BOOLEAN,
    error_kind TEXT,
    source_kind TEXT NOT NULL DEFAULT 'live',
    quality TEXT NOT NULL DEFAULT 'verified',
    partition_date DATE NOT NULL DEFAULT CURRENT_DATE,
    attachment_count INTEGER DEFAULT 0,
    attachment_total_bytes BIGINT DEFAULT 0,
    multimodal_types TEXT[] DEFAULT '{}',
    attempt_no INTEGER NOT NULL DEFAULT 0,
    tools JSONB NOT NULL DEFAULT '[]'::JSONB,
    title TEXT,
    summary TEXT,
    aggregate_applied_at TIMESTAMPTZ,
    t0_arrived_at TIMESTAMPTZ,
    t1_total_enqueued_at TIMESTAMPTZ,
    t2_total_dequeued_at TIMESTAMPTZ,
    t3_model_enqueued_at TIMESTAMPTZ,
    t4_model_dequeued_at TIMESTAMPTZ,
    t5_cred_enqueued_at TIMESTAMPTZ,
    t6_cred_dequeued_at TIMESTAMPTZ,
    t7_forward_start_at TIMESTAMPTZ,
    t8_response_start_at TIMESTAMPTZ,
    t9_response_end_at TIMESTAMPTZ,
    CONSTRAINT session_turns_hot_pkey PRIMARY KEY (id, partition_date),
    CONSTRAINT session_turns_hot_tenant_session_turn_key
        UNIQUE (tenant_id, session_id, turn_no, partition_date),
    CONSTRAINT session_turns_hot_tenant_request_key
        UNIQUE (tenant_id, request_id, partition_date),
    CONSTRAINT session_turns_hot_submit_mode_check
        CHECK (submit_mode IN ('full', 'delta', 'snapshot', 'inferred_compressed', 'attachment_only')),
    CONSTRAINT session_turns_hot_injection_verdict_check
        CHECK (injection_verdict IN ('pass', 'warn', 'block', 'skip')),
    CONSTRAINT session_turns_hot_output_verdict_check
        CHECK (output_verdict IN ('pass', 'warn', 'block', 'skip')),
    CONSTRAINT session_turns_hot_source_kind_check
        CHECK (source_kind IN ('live', 'backfill')),
    CONSTRAINT session_turns_hot_quality_check
        CHECK (quality IN ('verified', 'inferred', 'partial', 'rejected')),
    CONSTRAINT session_turns_hot_attachment_count_check
        CHECK (attachment_count IS NULL OR attachment_count >= 0),
    CONSTRAINT session_turns_hot_attachment_total_bytes_check
        CHECK (attachment_total_bytes IS NULL OR attachment_total_bytes >= 0),
    CONSTRAINT session_turns_hot_attempt_no_check
        CHECK (attempt_no >= 0)
) WITH (fillfactor = 90);


--
-- Name: TABLE session_turns_hot; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_turns_hot IS 'Independent heap write table for recent session turns. Cold rows are atomically moved to public.session_turns by promote_session_turns_hot_to_partition().';
