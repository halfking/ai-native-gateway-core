--
-- Name: route_incidents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.route_incidents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id text NOT NULL,
    endpoint_protocol text NOT NULL,
    model text NOT NULL,
    provider_id bigint,
    credential_id bigint,
    state text NOT NULL,
    failure_streak integer DEFAULT 0 NOT NULL,
    recovery_streak integer DEFAULT 0 NOT NULL,
    first_failure_at timestamp with time zone NOT NULL,
    last_failure_at timestamp with time zone,
    last_success_at timestamp with time zone,
    recovered_at timestamp with time zone,
    total_failures bigint DEFAULT 0 NOT NULL,
    total_successes bigint DEFAULT 0 NOT NULL,
    last_error_kind text,
    last_failure_stage text,
    resolution_source text,
    resolved_by_user text,
    resolved_reason text,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT route_incidents_state_check CHECK ((state = ANY (ARRAY['active'::text, 'recovering'::text, 'recovered'::text])))
);


--
-- Name: TABLE route_incidents; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.route_incidents IS 'Phase-1 read-only route incident aggregate. One active/recovering row per route key (tenant + protocol + model + provider + credential). Recovered rows are retained for timeline/audit. Cross-tenant reads return 404 at the API layer.';

