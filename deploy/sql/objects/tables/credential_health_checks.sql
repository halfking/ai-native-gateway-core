--
-- Name: credential_health_checks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_health_checks (
    id bigint NOT NULL,
    run_id bigint,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    provider_id bigint NOT NULL,
    credential_id bigint NOT NULL,
    models_ok boolean DEFAULT false NOT NULL,
    probe_ok boolean DEFAULT false NOT NULL,
    health_status text NOT NULL,
    warning_code text,
    classification_reason text,
    models_failure_reason text,
    models_http_status integer,
    probe_http_status integer,
    models_latency_ms integer,
    probe_latency_ms integer,
    probe_model text,
    models_error text,
    probe_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_credential_health_checks_models_failure_reason CHECK (((models_failure_reason IS NULL) OR (models_failure_reason = ANY (ARRAY['request_failed'::text, 'empty_models'::text, 'invalid_payload'::text, 'not_supported'::text])))),
    CONSTRAINT chk_credential_health_checks_status CHECK ((health_status = ANY (ARRAY['unknown'::text, 'healthy'::text, 'warning'::text, 'unreachable'::text])))
);

