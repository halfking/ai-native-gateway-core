--
-- Name: provider_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_events (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    event_kind text NOT NULL,
    payload_json jsonb,
    ts timestamp with time zone DEFAULT now() NOT NULL
);

