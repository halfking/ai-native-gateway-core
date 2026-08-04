--
-- Name: model_pricing_history; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_pricing_history (
    id integer NOT NULL,
    model_canonical character varying(64) NOT NULL,
    old_input_credits_per_1m bigint,
    old_output_credits_per_1m bigint,
    new_input_credits_per_1m bigint,
    new_output_credits_per_1m bigint,
    changed_by character varying(128),
    change_reason text,
    effective_date timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

