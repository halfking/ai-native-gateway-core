--
-- Name: session_turns; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_turns (
    id bigint NOT NULL,
    session_id text NOT NULL,
    turn_no integer NOT NULL,
    tenant_id character varying(255) NOT NULL,
    request_id text NOT NULL,
    project_id text,
    namespace text,
    parent_request_id text,
    task_type text,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    submit_mode text DEFAULT 'full'::text NOT NULL,
    compression_applied boolean DEFAULT false,
    compression_strategy text,
    compression_meta jsonb DEFAULT '{}'::jsonb,
    compression_tokens_saved integer,
    injection_verdict text DEFAULT 'skip'::text,
    output_verdict text DEFAULT 'skip'::text,
    model text,
    provider text,
    credential_id text,
    prompt_tokens integer,
    completion_tokens integer,
    cache_read_tokens integer,
    cache_write_tokens integer,
    cost_usd numeric(12,6),
    latency_ms integer,
    status_code integer,
    success boolean,
    error_kind text,
    source_kind text DEFAULT 'live'::text NOT NULL,
    quality text DEFAULT 'verified'::text NOT NULL,
    partition_date date DEFAULT CURRENT_DATE NOT NULL,
    attachment_count integer DEFAULT 0,
    attachment_total_bytes bigint DEFAULT 0,
    multimodal_types text[] DEFAULT '{}'::text[],
    attempt_no integer DEFAULT 0 NOT NULL,
    tools jsonb DEFAULT '[]'::jsonb NOT NULL,
    title text,
    summary text,
    aggregate_applied_at timestamp with time zone,
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
    CONSTRAINT session_turns_attachment_count_check CHECK (((attachment_count IS NULL) OR (attachment_count >= 0))),
    CONSTRAINT session_turns_attachment_total_bytes_check CHECK (((attachment_total_bytes IS NULL) OR (attachment_total_bytes >= 0))),
    CONSTRAINT session_turns_attempt_no_check CHECK ((attempt_no >= 0)),
    CONSTRAINT session_turns_injection_verdict_check CHECK ((injection_verdict = ANY (ARRAY['pass'::text, 'warn'::text, 'block'::text, 'skip'::text]))),
    CONSTRAINT session_turns_output_verdict_check CHECK ((output_verdict = ANY (ARRAY['pass'::text, 'warn'::text, 'block'::text, 'skip'::text]))),
    CONSTRAINT session_turns_quality_check CHECK ((quality = ANY (ARRAY['verified'::text, 'inferred'::text, 'partial'::text, 'rejected'::text]))),
    CONSTRAINT session_turns_source_kind_check CHECK ((source_kind = ANY (ARRAY['live'::text, 'backfill'::text]))),
    CONSTRAINT session_turns_submit_mode_check CHECK ((submit_mode = ANY (ARRAY['full'::text, 'delta'::text, 'snapshot'::text, 'inferred_compressed'::text, 'attachment_only'::text])))
)
PARTITION BY RANGE (partition_date);


--
-- Name: TABLE session_turns; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_turns IS 'V2轮次元数据表：存储每轮的元数据，正文存储在session_bodies。
     与request_logs并行，通过request_id关联便于数据校验。
     使用advisory lock保证turn_no在同一会话内单调递增。
     Created: 2026-07-17, Migration 430';

