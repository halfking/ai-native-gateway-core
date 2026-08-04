--
-- Name: task_default_routing_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.task_default_routing_audit (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    action text NOT NULL,
    routing_id bigint,
    task_type text,
    profile text,
    tier text,
    canonical_model text,
    tenant_id character varying(64),
    priority integer,
    reason text,
    expires_at timestamp with time zone,
    old_expires_at timestamp with time zone,
    actor text,
    CONSTRAINT task_default_routing_audit_action_check CHECK ((action = ANY (ARRAY['insert'::text, 'update'::text, 'delete'::text])))
);


--
-- Name: TABLE task_default_routing_audit; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.task_default_routing_audit IS 'task_default_routing 的变更审计；actor 必须是已认证的 super_admin 或 tenant_admin（后者仅可见本租户行）。';

