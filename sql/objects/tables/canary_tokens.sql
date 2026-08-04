--
-- Name: canary_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.canary_tokens (
    id integer NOT NULL,
    tenant_id character varying(255) DEFAULT 'default'::character varying NOT NULL,
    token_value character varying(255) NOT NULL,
    token_type character varying(50) DEFAULT 'uuid'::character varying,
    token_name character varying(100),
    prompt_template_id character varying(255),
    description text,
    leak_action public.injection_action DEFAULT 'block'::public.injection_action,
    notify_on_leak boolean DEFAULT true,
    active boolean DEFAULT true,
    expires_at timestamp with time zone,
    times_injected integer DEFAULT 0,
    times_leaked integer DEFAULT 0,
    last_leaked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    created_by character varying(255)
);


--
-- Name: TABLE canary_tokens; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.canary_tokens IS 'Canary Token 配置 - 检测提示词泄漏';

