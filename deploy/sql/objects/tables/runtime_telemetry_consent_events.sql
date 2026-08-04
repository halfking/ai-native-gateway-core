--
-- Name: runtime_telemetry_consent_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.runtime_telemetry_consent_events (
    id bigint NOT NULL,
    hardware_hash text NOT NULL,
    license_id bigint NOT NULL,
    enabled boolean NOT NULL,
    agreement_version text NOT NULL,
    operator_user_id bigint NOT NULL,
    source text NOT NULL,
    occurred_at timestamp with time zone NOT NULL
);

