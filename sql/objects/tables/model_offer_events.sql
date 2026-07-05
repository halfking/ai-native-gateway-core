--
-- Name: model_offer_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_offer_events (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    source text NOT NULL,
    action text NOT NULL,
    credential_id bigint NOT NULL,
    provider_id bigint,
    canonical_id bigint,
    raw_model_name text NOT NULL,
    reason_code text,
    reason_detail text,
    request_id text,
    run_id bigint,
    metadata_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT model_offer_events_action_check CHECK ((action = ANY (ARRAY['disable'::text, 'enable'::text]))),
    CONSTRAINT model_offer_events_source_check CHECK ((source = ANY (ARRAY['runtime'::text, 'discovery'::text, 'admin'::text, 'migration'::text, 'manual'::text])))
);

