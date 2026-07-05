--
-- Name: topup_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.topup_packages (
    id integer NOT NULL,
    code character varying(32) NOT NULL,
    tier character varying(16) NOT NULL,
    name character varying(128) NOT NULL,
    price_cents integer NOT NULL,
    credits_amount bigint NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT topup_packages_tier_check CHECK (((tier)::text = ANY (ARRAY[('small'::character varying)::text, ('medium'::character varying)::text, ('large'::character varying)::text])))
);

