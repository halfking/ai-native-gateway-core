--
-- Name: licenses; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.licenses (
    id bigint NOT NULL,
    license_key text NOT NULL,
    customer_name text DEFAULT ''::text NOT NULL,
    customer_email text DEFAULT ''::text NOT NULL,
    max_devices integer DEFAULT 2 NOT NULL,
    subscription_tier text DEFAULT 'starter'::text NOT NULL,
    features jsonb DEFAULT '[]'::jsonb NOT NULL,
    expires_at timestamp with time zone,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    holder_id bigint
);

