--
-- Name: model_name_mapping; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_name_mapping (
    id bigint NOT NULL,
    raw_model_name text NOT NULL,
    standardized_name text NOT NULL,
    description text,
    auto_generated boolean DEFAULT false,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by text
);


--
-- Name: TABLE model_name_mapping; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.model_name_mapping IS 'Maps raw model names (from provider APIs) to standardized names. Used when provider_models.standardized_name is empty.';

