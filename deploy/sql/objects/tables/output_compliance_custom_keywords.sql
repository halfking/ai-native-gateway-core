--
-- Name: output_compliance_custom_keywords; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.output_compliance_custom_keywords (
    id integer NOT NULL,
    tenant_id character varying(255) DEFAULT 'default'::character varying NOT NULL,
    keyword character varying(200) NOT NULL,
    category character varying(50) DEFAULT 'custom'::character varying NOT NULL,
    severity integer DEFAULT 7 NOT NULL,
    action character varying(20) DEFAULT 'warn'::character varying,
    enabled boolean DEFAULT true,
    description text,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    created_by character varying(255),
    updated_by character varying(255),
    CONSTRAINT output_compliance_custom_keywords_action_check CHECK (((action)::text = ANY ((ARRAY['log'::character varying, 'warn'::character varying, 'redact'::character varying, 'block'::character varying])::text[]))),
    CONSTRAINT output_compliance_custom_keywords_severity_check CHECK (((severity >= 1) AND (severity <= 10)))
);


--
-- Name: TABLE output_compliance_custom_keywords; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.output_compliance_custom_keywords IS '输出合规自定义敏感词库 - 租户级';

