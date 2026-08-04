--
-- Name: tier_module_map; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tier_module_map (
    tier_code text NOT NULL,
    module_key text NOT NULL,
    max_features text,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

