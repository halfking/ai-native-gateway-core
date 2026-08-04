--
-- Name: assets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.assets (
    kind text NOT NULL,
    ref_id bigint NOT NULL,
    tenant_id text NOT NULL,
    name text NOT NULL,
    owner text,
    team text,
    cost_center text,
    tags jsonb DEFAULT '{}'::jsonb NOT NULL,
    health_state text DEFAULT 'unknown'::text NOT NULL,
    version text DEFAULT '0.0.0'::text NOT NULL,
    registered_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT chk_assets_health CHECK ((health_state = ANY (ARRAY['healthy'::text, 'degraded'::text, 'down'::text, 'unknown'::text]))),
    CONSTRAINT chk_assets_kind CHECK ((kind = ANY (ARRAY['llm_endpoint'::text, 'mcp_server'::text, 'agent'::text])))
);

