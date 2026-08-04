--
-- Name: provider_credibility_tests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_credibility_tests (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    provider_id bigint NOT NULL,
    model_name text NOT NULL,
    test_time timestamp with time zone NOT NULL,
    test_type text NOT NULL,
    authenticity_score numeric(5,2),
    compliance_score numeric(5,2),
    consistency_score numeric(5,2),
    version_score numeric(5,2),
    test_details jsonb,
    anomalies jsonb,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE provider_credibility_tests; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_credibility_tests IS '模型可信度测试详细记录';

