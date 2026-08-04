--
-- Name: tool_usage_stats; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_usage_stats (
    id bigint NOT NULL,
    tool_id character varying NOT NULL,
    tenant_id character varying NOT NULL,
    usage_date date NOT NULL,
    call_count bigint DEFAULT 0,
    success_count bigint DEFAULT 0,
    error_count bigint DEFAULT 0,
    avg_latency_ms integer,
    last_called_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone
)
PARTITION BY RANGE (created_at);

