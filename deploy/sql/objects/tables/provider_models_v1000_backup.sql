--
-- Name: provider_models_v1000_backup; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_models_v1000_backup (
    provider_model_id bigint NOT NULL,
    canonical_id bigint,
    standardized_name text,
    outbound_model_name text
);

