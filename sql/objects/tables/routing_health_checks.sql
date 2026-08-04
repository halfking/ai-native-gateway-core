--
-- Name: routing_health_checks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_health_checks (
    id bigint NOT NULL,
    check_id text NOT NULL,
    severity text DEFAULT 'warning'::text NOT NULL,
    entity_type text NOT NULL,
    entity_id bigint,
    entity_name text DEFAULT ''::text NOT NULL,
    detail text DEFAULT ''::text NOT NULL,
    fix_sql text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    auto_fixed_at timestamp with time zone,
    auto_fix_result text,
    dismissed_at timestamp with time zone,
    dismissed_by text,
    dismissed_reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT routing_health_checks_severity_check CHECK ((severity = ANY (ARRAY['critical'::text, 'warning'::text, 'info'::text]))),
    CONSTRAINT routing_health_checks_status_check CHECK ((status = ANY (ARRAY['open'::text, 'auto_fixed'::text, 'manual_fixed'::text, 'dismissed'::text])))
);


--
-- Name: TABLE routing_health_checks; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.routing_health_checks IS '路由健康检查发现问题（自动检查 → 预警 → 修复/忽略）';

