--
-- Name: diagnostic_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.diagnostic_runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    incident_id uuid,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    kind text NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    heartbeat_at timestamp with time zone,
    trigger_source text,
    error text,
    summary_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT diagnostic_runs_kind_check CHECK ((kind = ANY (ARRAY['direct_upstream_test'::text, 'through_gateway_test'::text, 'reprobe'::text, 'release_slot'::text, 'reset_slots'::text, 'reset_availability'::text, 'recover'::text]))),
    CONSTRAINT diagnostic_runs_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'running'::text, 'succeeded'::text, 'failed'::text, 'cancelled'::text])))
);


--
-- Name: TABLE diagnostic_runs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.diagnostic_runs IS 'Phase-2 sanitized results of a single diagnostic test. request body, response body, auth headers, raw upstream errors, and the upstream URL are NEVER stored. The route key is recorded so the result can be associated with a specific incident.';

