--
-- Name: model_probe_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_probe_state (
    credential_id bigint NOT NULL,
    raw_model_name text NOT NULL,
    state text DEFAULT 'unknown'::text NOT NULL,
    consecutive_successes integer DEFAULT 0 NOT NULL,
    consecutive_failures integer DEFAULT 0 NOT NULL,
    total_attempts integer DEFAULT 0 NOT NULL,
    last_attempt_at timestamp with time zone,
    next_retry_at timestamp with time zone DEFAULT now() NOT NULL,
    last_status text,
    last_state_change_at timestamp with time zone,
    last_state_change_run bigint,
    last_unavailable_reason text,
    last_err_code text,
    next_retry_at_override timestamp with time zone,
    state_expires_at timestamp with time zone,
    marked_suspicious_at timestamp with time zone,
    probing_started_at timestamp with time zone,
    probing_credential_concurrency integer DEFAULT 0,
    probe_priority text DEFAULT 'watchdog'::text,
    last_verified_at timestamp with time zone,
    verification_interval interval DEFAULT '04:00:00'::interval,
    success_rate_7d numeric(5,2) DEFAULT 0.00,
    consecutive_watchdog_successes integer DEFAULT 0,
    last_real_request_at timestamp with time zone,
    real_request_success_count integer DEFAULT 0,
    real_request_failure_count integer DEFAULT 0,
    verification_attempt_1_at timestamp with time zone,
    verification_attempt_2_at timestamp with time zone,
    verification_result_1 boolean,
    verification_result_2 boolean,
    verification_latency_1_ms integer,
    verification_latency_2_ms integer,
    CONSTRAINT check_probe_priority CHECK ((probe_priority = ANY (ARRAY['urgent'::text, 'suspicious'::text, 'failing'::text, 'recovering'::text, 'watchdog'::text])))
);


--
-- Name: TABLE model_probe_state; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.model_probe_state IS 'Per-(credential, model) probe consensus state. 3 consecutive successes to recover; 3 consecutive failures to confirm-broken.';

