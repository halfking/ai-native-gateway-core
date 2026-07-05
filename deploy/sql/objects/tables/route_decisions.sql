--
-- Name: route_decisions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.route_decisions (
    id bigint NOT NULL,
    request_id text,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id text,
    api_key_id bigint,
    canonical_id bigint,
    selected_credential_id bigint,
    candidates_json jsonb,
    reason text,
    sticky_hit boolean
);

