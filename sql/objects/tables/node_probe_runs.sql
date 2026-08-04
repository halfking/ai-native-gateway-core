--
-- Name: node_probe_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.node_probe_runs (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    raw_model_name text NOT NULL,
    trigger_kind text NOT NULL,
    trigger_request_id text,
    attempt integer NOT NULL,
    next_retry_seconds integer NOT NULL,
    direct_ok boolean NOT NULL,
    direct_http_status integer,
    direct_err_code text,
    direct_latency_ms integer,
    direct_err_detail text,
    gateway_ok boolean NOT NULL,
    gateway_http_status integer,
    gateway_err_code text,
    gateway_latency_ms integer,
    gateway_err_detail text,
    success boolean NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    duration_ms integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    api_model text,
    outbound_model text,
    provider_id bigint,
    request_url text,
    request_headers jsonb,
    request_body text,
    response_body text,
    timeout_at_ms integer,
    via_proxy boolean,
    CONSTRAINT node_probe_runs_attempt_check CHECK (((attempt >= 1) AND (attempt <= 7))),
    CONSTRAINT node_probe_runs_trigger_kind_check CHECK ((trigger_kind = ANY (ARRAY['request_failure'::text, 'manual'::text, 'credential_recovery'::text, 'sync_request'::text])))
);

