--
-- Name: model_discovery_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_discovery_runs (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    trigger text DEFAULT 'manual'::text NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    heartbeat_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_expires_at timestamp with time zone NOT NULL,
    requested_by text,
    request_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    summary_json jsonb,
    error text,
    CONSTRAINT chk_model_discovery_runs_status CHECK ((status = ANY (ARRAY['running'::text, 'succeeded'::text, 'failed'::text]))),
    CONSTRAINT chk_model_discovery_runs_trigger CHECK ((trigger = ANY (ARRAY['manual'::text, 'scheduled'::text, 'credential_added'::text])))
);

