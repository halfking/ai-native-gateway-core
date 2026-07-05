--
-- Name: tool_categories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_categories (
    id character varying(64) NOT NULL,
    name character varying(128) NOT NULL,
    description text,
    enabled boolean DEFAULT true,
    display_order integer DEFAULT 0,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE tool_categories; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.tool_categories IS 'Phase 2: Tool category definitions for layered loading';

