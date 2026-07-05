--
-- Name: credential_capabilities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_capabilities (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    capability text NOT NULL,
    supported boolean DEFAULT false NOT NULL,
    last_tested_at timestamp with time zone,
    evidence_json jsonb,
    CONSTRAINT credential_capabilities_capability_check CHECK ((capability = ANY (ARRAY['tool_use'::text, 'vision'::text, 'streaming'::text, 'prompt_caching'::text, 'structured_output'::text, 'long_context'::text, 'json_mode'::text, 'batch'::text])))
);

