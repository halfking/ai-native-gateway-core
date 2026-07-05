--
-- Name: model_probe_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_probe_runs (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    credential_id bigint NOT NULL,
    raw_model_name text NOT NULL,
    status text NOT NULL,
    http_status integer,
    error_code text,
    error_message text,
    latency_ms integer DEFAULT 0 NOT NULL,
    state_change text,
    state_applied boolean DEFAULT true NOT NULL,
    triggered_by text DEFAULT 'scheduler'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

