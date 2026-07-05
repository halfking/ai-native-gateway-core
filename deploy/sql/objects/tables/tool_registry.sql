--
-- Name: tool_registry; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_registry (
    id integer NOT NULL,
    category character varying(64) NOT NULL,
    tool_name character varying(128) NOT NULL,
    tool_definition jsonb NOT NULL,
    enabled boolean DEFAULT true,
    priority integer DEFAULT 0,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    tool_id character varying(128) NOT NULL,
    tenant_id character varying(64) DEFAULT 'default'::character varying,
    version integer DEFAULT 1,
    deprecation_date timestamp with time zone,
    min_client_version character varying(32),
    breaking_changes jsonb DEFAULT '[]'::jsonb,
    superseded_by character varying(128)
);


--
-- Name: TABLE tool_registry; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.tool_registry IS 'Phase 2: Centralized tool definition registry';

