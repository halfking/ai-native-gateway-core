--
-- Name: task_default_routing; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.task_default_routing (
    id bigint NOT NULL,
    task_type text NOT NULL,
    profile text DEFAULT ''::text NOT NULL,
    tier text DEFAULT 'primary'::text NOT NULL,
    canonical_model text NOT NULL,
    tenant_id character varying(64),
    priority integer DEFAULT 100 NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    created_by text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone,
    CONSTRAINT task_default_routing_profile_check CHECK ((profile = ANY (ARRAY[''::text, 'smart'::text, 'speed_first'::text, 'cost_first'::text]))),
    CONSTRAINT task_default_routing_tier_check CHECK ((tier = ANY (ARRAY['primary'::text, 'secondary'::text, 'fallback'::text])))
);


--
-- Name: TABLE task_default_routing; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.task_default_routing IS 'Auto 路由显式默认：为 (task_type, profile, tenant) 指定首选/兜底模型。tenant_id NULL=平台默认。Resolve 优先级：tenant+profile > tenant+通用 > platform+profile > platform+通用。';

