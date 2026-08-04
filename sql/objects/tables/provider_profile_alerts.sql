--
-- Name: provider_profile_alerts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_profile_alerts (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    provider_id bigint NOT NULL,
    alert_type text NOT NULL,
    alert_level text NOT NULL,
    trigger_date date NOT NULL,
    current_score numeric(5,2),
    previous_score numeric(5,2),
    score_change numeric(5,2),
    dimension text,
    message text NOT NULL,
    details jsonb,
    action_taken text,
    resolved_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE provider_profile_alerts; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_profile_alerts IS '供应商画像告警和自动处理记录';

