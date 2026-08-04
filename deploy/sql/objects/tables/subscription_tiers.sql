--
-- Name: subscription_tiers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.subscription_tiers (
    id integer NOT NULL,
    code text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    price_cents integer DEFAULT 0 NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

