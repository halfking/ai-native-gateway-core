--
-- Name: credential_probe_model_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_probe_model_log (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    credential_id bigint NOT NULL,
    source text NOT NULL,
    old_model text,
    new_model text,
    actor text,
    reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

