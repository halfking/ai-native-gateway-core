--
-- Name: request_stage_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_stage_events (
    id bigint NOT NULL,
    request_id text NOT NULL,
    tenant_id text NOT NULL,
    seq integer NOT NULL,
    stage text NOT NULL,
    stage_name text,
    module text,
    event_timestamp timestamp with time zone NOT NULL,
    duration_ms integer,
    status text NOT NULL,
    error_message text,
    http_status integer,
    response_body text,
    failure_hint text,
    details jsonb,
    snapshot jsonb,
    redis_hit boolean,
    redis_key text,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE request_stage_events; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.request_stage_events IS '请求阶段事件扁平化表。补充 trace_events JSONB，用于高效查询和聚合。';

