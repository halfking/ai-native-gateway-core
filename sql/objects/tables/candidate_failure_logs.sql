--
-- Name: candidate_failure_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.candidate_failure_logs (
    id bigint NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    credential_id integer NOT NULL,
    provider_id integer NOT NULL,
    raw_model_name text NOT NULL,
    attempt_index integer DEFAULT 0 NOT NULL,
    error_kind text NOT NULL,
    error_message text,
    upstream_status_code integer,
    upstream_response_body text,
    upstream_response_preview text,
    latency_ms integer,
    retryable boolean,
    context jsonb
);


--
-- Name: TABLE candidate_failure_logs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.candidate_failure_logs IS 'Per-credential, per-model upstream failure log. Populated by routing/executor.go on every failed candidate attempt so transient diagnostics surface the actual vendor response (kind, status, body) instead of a generic "all N candidates failed" message. Used by candidate_failure_monitor for alerts and the admin candidate-failure API.';

