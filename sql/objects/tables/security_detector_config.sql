--
-- Name: security_detector_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.security_detector_config (
    id integer NOT NULL,
    tenant_id text,
    config_name text DEFAULT 'default'::text NOT NULL,
    description text,
    sensitive_words jsonb DEFAULT '[]'::jsonb NOT NULL,
    injection_patterns jsonb DEFAULT '[]'::jsonb NOT NULL,
    pii_patterns jsonb DEFAULT '[]'::jsonb NOT NULL,
    jailbreak_patterns jsonb DEFAULT '[]'::jsonb NOT NULL,
    max_content_len integer DEFAULT 50000 NOT NULL,
    score_threshold_log integer DEFAULT 3 NOT NULL,
    score_threshold_warn integer DEFAULT 5 NOT NULL,
    score_threshold_approval integer DEFAULT 8 NOT NULL,
    score_threshold_block integer DEFAULT 10 NOT NULL,
    severity_threshold_approval integer DEFAULT 8 NOT NULL,
    audit_enabled boolean DEFAULT true NOT NULL,
    audit_sampling_rate double precision DEFAULT 1.0 NOT NULL,
    auto_approval_whitelist jsonb DEFAULT '[]'::jsonb,
    enabled boolean DEFAULT true NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT security_detector_config_audit_sampling_rate_check CHECK (((audit_sampling_rate >= (0)::double precision) AND (audit_sampling_rate <= (1)::double precision))),
    CONSTRAINT security_detector_config_score_threshold_approval_check CHECK (((score_threshold_approval >= 0) AND (score_threshold_approval <= 10))),
    CONSTRAINT security_detector_config_score_threshold_block_check CHECK (((score_threshold_block >= 0) AND (score_threshold_block <= 10))),
    CONSTRAINT security_detector_config_score_threshold_log_check CHECK (((score_threshold_log >= 0) AND (score_threshold_log <= 10))),
    CONSTRAINT security_detector_config_score_threshold_warn_check CHECK (((score_threshold_warn >= 0) AND (score_threshold_warn <= 10))),
    CONSTRAINT security_detector_config_severity_threshold_approval_check CHECK (((severity_threshold_approval >= 0) AND (severity_threshold_approval <= 10)))
);


--
-- Name: TABLE security_detector_config; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.security_detector_config IS '安全检测器配置 — 统一管理提示词注入检测和会话审计配置，支持租户级定制和热更新';

