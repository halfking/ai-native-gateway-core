--
-- Name: provider_error_details; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_error_details (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    model_name character varying(100),
    endpoint character varying(50),
    error_type character varying(50) NOT NULL,
    error_code character varying(50),
    error_message text,
    request_id character varying(100),
    user_id character varying(100),
    tenant_id character varying(100),
    input_tokens integer,
    context jsonb,
    occurrences integer DEFAULT 1,
    first_seen_at timestamp with time zone NOT NULL,
    last_seen_at timestamp with time zone NOT NULL,
    acknowledged boolean DEFAULT false,
    resolved boolean DEFAULT false,
    resolution_note text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: TABLE provider_error_details; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_error_details IS '供应商错误详情聚合表 - 用于根因分析和错误趋势';

