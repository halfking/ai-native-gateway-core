--
-- Name: self_check_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.self_check_runs (
    id bigint NOT NULL,
    model_name text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    duration_ms integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    rounds_total integer DEFAULT 3 NOT NULL,
    rounds_success integer DEFAULT 0 NOT NULL,
    had_tool_call boolean DEFAULT false NOT NULL,
    total_tokens integer DEFAULT 0 NOT NULL,
    avg_latency_ms integer DEFAULT 0 NOT NULL,
    error_type text,
    error_detail text,
    upstream_tested boolean DEFAULT false NOT NULL,
    upstream_result text,
    upstream_latency_ms integer,
    upstream_error text,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    selection_strategy text DEFAULT 'most_used'::text,
    attempted_models jsonb DEFAULT '[]'::jsonb,
    CONSTRAINT self_check_runs_error_type_check CHECK (((error_type IS NULL) OR (error_type = ANY (ARRAY['http_000'::text, 'http_502'::text, 'http_503'::text, 'http_504'::text, 'timeout'::text, 'upstream_fail'::text, 'none'::text])))),
    CONSTRAINT self_check_runs_selection_strategy_check CHECK (((selection_strategy IS NULL) OR (selection_strategy ~~ 'most_used'::text) OR (selection_strategy ~~ 'fallback_%'::text) OR (selection_strategy = 'random'::text))),
    CONSTRAINT self_check_runs_status_check CHECK ((status = ANY (ARRAY['running'::text, 'success'::text, 'partial'::text, 'failed'::text, 'retrying'::text])))
);

