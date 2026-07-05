--
-- Name: tool_usage_stats; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_usage_stats (
    id bigint NOT NULL,
    tool_id character varying(128) NOT NULL,
    tenant_id character varying(64) DEFAULT 'default'::character varying NOT NULL,
    usage_date date DEFAULT CURRENT_DATE NOT NULL,
    call_count bigint DEFAULT 0 NOT NULL,
    success_count bigint DEFAULT 0 NOT NULL,
    error_count bigint DEFAULT 0 NOT NULL,
    avg_latency_ms integer DEFAULT 0,
    last_called_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.tool_usage_stats FORCE ROW LEVEL SECURITY;

