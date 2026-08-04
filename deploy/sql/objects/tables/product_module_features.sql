--
-- Name: product_module_features; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.product_module_features (
    id integer NOT NULL,
    module_key text NOT NULL,
    feature_key text NOT NULL,
    feature_name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    setting_key text,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

