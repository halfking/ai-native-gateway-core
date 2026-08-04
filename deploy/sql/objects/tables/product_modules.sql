--
-- Name: product_modules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.product_modules (
    id integer NOT NULL,
    key text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    category text NOT NULL,
    icon text,
    setting_key text,
    is_base boolean DEFAULT false NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

