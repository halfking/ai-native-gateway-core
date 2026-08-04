--
-- Name: agents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agents (
    id bigint NOT NULL,
    tenant_id text NOT NULL,
    name text NOT NULL,
    kind text NOT NULL,
    endpoint text NOT NULL,
    status text DEFAULT 'unknown'::text NOT NULL,
    capabilities jsonb DEFAULT '{}'::jsonb NOT NULL,
    version text DEFAULT '0.0.0'::text NOT NULL,
    auth_scheme text,
    last_heartbeat timestamp with time zone,
    registered_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT chk_agents_auth CHECK (((auth_scheme IS NULL) OR (auth_scheme = ANY (ARRAY['bearer'::text, 'api_key'::text, 'mtls'::text, 'none'::text])))),
    CONSTRAINT chk_agents_kind CHECK ((kind = ANY (ARRAY['openclaw'::text, 'brandmind-go'::text, 'crm-go'::text, 'custom'::text]))),
    CONSTRAINT chk_agents_status CHECK ((status = ANY (ARRAY['healthy'::text, 'degraded'::text, 'down'::text, 'unknown'::text])))
);

