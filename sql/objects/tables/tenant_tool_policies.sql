--
-- Name: tenant_tool_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_tool_policies (
    id bigint NOT NULL,
    tenant_id character varying(64) NOT NULL,
    tool_pattern character varying(128) NOT NULL,
    policy_type character varying(16) NOT NULL,
    reason character varying(256),
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by character varying(128),
    CONSTRAINT chk_policy_type CHECK (((policy_type)::text = ANY (ARRAY[('allow'::character varying)::text, ('deny'::character varying)::text])))
);

ALTER TABLE ONLY public.tenant_tool_policies FORCE ROW LEVEL SECURITY;

