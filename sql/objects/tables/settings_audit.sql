--
-- Name: settings_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.settings_audit (
    id bigint NOT NULL,
    setting_key character varying(128) NOT NULL,
    tenant_id character varying(64),
    action character varying(16) NOT NULL,
    old_value jsonb,
    new_value jsonb,
    operator_user character varying(64) NOT NULL,
    operator_role character varying(32) NOT NULL,
    confirm_token character varying(64),
    client_ip character varying(45),
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.settings_audit FORCE ROW LEVEL SECURITY;


--
-- Name: TABLE settings_audit; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.settings_audit IS '设置修改审计日志（bg/settings_audit_cleaner.go 每 24h 清理 7 天前的数据）';

