--
-- Name: provider_profile_whitelist; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_profile_whitelist (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    reason text,
    added_by text,
    added_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE provider_profile_whitelist; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_profile_whitelist IS '供应商自动处理白名单（白名单中的供应商不会被自动禁用）';

