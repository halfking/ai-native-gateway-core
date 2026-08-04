--
-- Name: maas_credit_consumption_buckets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.maas_credit_consumption_buckets (
    tenant_id text NOT NULL,
    bucket_start timestamp with time zone NOT NULL,
    credits bigint DEFAULT 0 NOT NULL,
    request_count integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

