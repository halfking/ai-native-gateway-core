--
-- Name: credential_model_capabilities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_capabilities (
    id bigint NOT NULL,
    credential_model_binding_id bigint NOT NULL,
    capability text NOT NULL,
    supported boolean DEFAULT false NOT NULL,
    last_tested_at timestamp with time zone,
    evidence_json jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT credential_model_capabilities_capability_check CHECK ((capability = 'native_responses_nonstream'::text))
);
