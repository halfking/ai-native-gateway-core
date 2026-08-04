--
-- Name: agent_relationships; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_relationships (
    src_agent_id bigint NOT NULL,
    dst_agent_id bigint NOT NULL,
    rel text NOT NULL,
    weight double precision DEFAULT 1.0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_agent_rel CHECK ((rel = ANY (ARRAY['calls'::text, 'delegates'::text, 'depends_on'::text, 'similar_to'::text]))),
    CONSTRAINT chk_agent_rel_no_self CHECK ((src_agent_id <> dst_agent_id))
);

