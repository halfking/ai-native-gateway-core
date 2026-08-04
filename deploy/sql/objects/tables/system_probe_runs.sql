--
-- Name: system_probe_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.system_probe_runs (
    id bigint NOT NULL,
    task_id bigint NOT NULL,
    task_type text NOT NULL,
    automaticity text DEFAULT 'mandatory'::text NOT NULL,
    credential_id bigint NOT NULL,
    provider_id bigint,
    raw_model text NOT NULL,
    source text NOT NULL,
    worker_id text,
    status text NOT NULL,
    attempt integer DEFAULT 1 NOT NULL,
    max_attempts integer DEFAULT 3 NOT NULL,
    http_status integer,
    latency_ms integer,
    dns_ms integer,
    tls_ms integer,
    request_url text,
    request_body_preview text,
    response_body_preview text,
    err_code text,
    err_detail text,
    skip_reason text,
    recent_request_id text,
    recent_request_at timestamp with time zone,
    started_at timestamp with time zone NOT NULL,
    finished_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT system_probe_runs_attempt_check CHECK (((attempt >= 1) AND (max_attempts >= 1))),
    CONSTRAINT system_probe_runs_automaticity_check CHECK ((automaticity = ANY (ARRAY['mandatory'::text, 'automatic'::text]))),
    CONSTRAINT system_probe_runs_status_check CHECK ((status = ANY (ARRAY['success'::text, 'failed'::text, 'expired'::text, 'skipped'::text, 'timeout'::text, 'network_error'::text]))),
    CONSTRAINT system_probe_runs_task_type_check CHECK ((task_type = ANY (ARRAY['direct_ping'::text, 'gateway_ping'::text, 'chat_minimal'::text, 'chat_tool'::text, 'chat_stream'::text, 'http_ping'::text])))
)
PARTITION BY RANGE (created_at);


--
-- Name: TABLE system_probe_runs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.system_probe_runs IS '344: 系统监测模块审计表。任务唯一身份 = task_id (Redis INCR)。按天分区，30d 滚动。';

