--
-- Name: credential_probe_configs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_probe_configs (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    probe_model text NOT NULL,
    priority integer DEFAULT 1,
    enabled boolean DEFAULT true,
    created_at timestamp with time zone DEFAULT now()
);

