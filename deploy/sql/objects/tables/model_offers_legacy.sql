--
-- Name: model_offers_legacy; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_offers_legacy (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint,
    raw_model_name text NOT NULL,
    p95_latency_ms integer,
    success_rate numeric(5,4),
    available boolean DEFAULT true NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    routing_tier smallint DEFAULT 2,
    weight smallint DEFAULT 100,
    unit_price_in_per_1m numeric(12,6),
    unit_price_out_per_1m numeric(12,6),
    currency text DEFAULT 'USD'::text,
    outbound_model_name text,
    cache_read_price_per_1m numeric(12,6),
    cache_write_price_per_1m numeric(12,6),
    standardized_name text,
    unavailable_reason text,
    unavailable_at timestamp with time zone,
    billing_mode text DEFAULT 'per_token'::text,
    pricing_source text,
    pricing_updated_at timestamp with time zone,
    manual_priority smallint DEFAULT 99,
    active_sessions integer DEFAULT 0,
    consecutive_failures integer DEFAULT 0,
    CONSTRAINT model_offers_manual_priority_chk CHECK (((manual_priority >= 1) AND (manual_priority <= 99))),
    CONSTRAINT model_offers_routing_tier_chk CHECK (((routing_tier >= 1) AND (routing_tier <= 9))),
    CONSTRAINT model_offers_weight_chk CHECK (((weight >= 1) AND (weight <= 1000)))
);

