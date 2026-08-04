--
-- Name: runtime_telemetry_preferences; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.runtime_telemetry_preferences (
    hardware_hash text NOT NULL,
    license_id bigint NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    agreement_version text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    disabled_at timestamp with time zone
);

