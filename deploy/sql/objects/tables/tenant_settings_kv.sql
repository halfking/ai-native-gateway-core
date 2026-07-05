--
-- Name: tenant_settings_kv; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_settings_kv (
    tenant_id character varying(64) NOT NULL,
    key character varying(128) NOT NULL,
    value jsonb NOT NULL,
    value_type character varying(32) NOT NULL,
    category character varying(32) DEFAULT 'general'::character varying NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by character varying(64),
    prev_value jsonb,
    prev_updated_at timestamp with time zone
);

ALTER TABLE ONLY public.tenant_settings_kv FORCE ROW LEVEL SECURITY;


--
-- Name: TABLE tenant_settings_kv; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.tenant_settings_kv IS '租户级运行时设置（Q3）';

