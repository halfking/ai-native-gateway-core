--
-- Name: severity_action_matrix; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.severity_action_matrix (
    id integer NOT NULL,
    tenant_id character varying(255) DEFAULT 'default'::character varying NOT NULL,
    severity_level character varying(20) NOT NULL,
    observe_action public.injection_action DEFAULT 'log'::public.injection_action,
    enforce_action public.injection_action DEFAULT 'block'::public.injection_action,
    require_approval boolean DEFAULT false,
    approval_timeout_minutes integer DEFAULT 0,
    notify_on_detect boolean DEFAULT false,
    notify_channels jsonb DEFAULT '[]'::jsonb,
    affect_session_health boolean DEFAULT true,
    session_health_penalty integer DEFAULT 10,
    terminate_session_on_repeat boolean DEFAULT false,
    repeat_threshold integer DEFAULT 3,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    CONSTRAINT severity_action_matrix_repeat_threshold_check CHECK ((repeat_threshold > 0)),
    CONSTRAINT severity_action_matrix_session_health_penalty_check CHECK (((session_health_penalty >= 0) AND (session_health_penalty <= 100))),
    CONSTRAINT valid_severity CHECK (((severity_level)::text = ANY ((ARRAY['low'::character varying, 'medium'::character varying, 'high'::character varying, 'critical'::character varying])::text[])))
);


--
-- Name: TABLE severity_action_matrix; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.severity_action_matrix IS '严重等级处理矩阵 - 配置不同风险等级的处理动作';

