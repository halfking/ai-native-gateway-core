--
-- Name: settings_kv; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.settings_kv (
    key character varying(128) NOT NULL,
    value jsonb NOT NULL,
    value_type character varying(32) NOT NULL,
    scope character varying(16) DEFAULT 'platform'::character varying NOT NULL,
    category character varying(32) DEFAULT 'general'::character varying NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by character varying(64),
    prev_value jsonb,
    prev_updated_at timestamp with time zone
);


--
-- Name: TABLE settings_kv; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.settings_kv IS '平台级运行时设置（Q2: 立即生效）';

