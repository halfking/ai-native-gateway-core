--
-- Name: provider_cost_reconciliation; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_cost_reconciliation (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    reconciliation_month date NOT NULL,
    gateway_total_tokens bigint,
    gateway_input_tokens bigint,
    gateway_output_tokens bigint,
    gateway_total_cost numeric(12,4),
    provider_total_tokens bigint,
    provider_input_tokens bigint,
    provider_output_tokens bigint,
    provider_total_cost numeric(12,4),
    token_diff_rate numeric(5,4),
    cost_diff_rate numeric(5,4),
    data_source text,
    notes text,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE provider_cost_reconciliation; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_cost_reconciliation IS '供应商费用对账数据（月度）';

