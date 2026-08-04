--
-- Name: provider_profile_daily; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_profile_daily (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    provider_id bigint NOT NULL,
    profile_date date NOT NULL,
    network_score numeric(5,2),
    credibility_score numeric(5,2),
    availability_score numeric(5,2),
    stability_score numeric(5,2),
    scale_score numeric(5,2),
    cost_accuracy_score numeric(5,2),
    price_score numeric(5,2),
    total_score numeric(5,2),
    timeslot_scores jsonb,
    score_stddev numeric(5,2),
    best_timeslot text,
    worst_timeslot text,
    raw_stats jsonb,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE provider_profile_daily; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_profile_daily IS '供应商画像天级聚合数据和评分（保留365天）';

