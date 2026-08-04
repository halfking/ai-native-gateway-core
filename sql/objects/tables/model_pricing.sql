--
-- Name: model_pricing; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_pricing (
    id integer NOT NULL,
    model_canonical character varying(64) NOT NULL,
    display_name character varying(128) NOT NULL,
    input_credits_per_1m bigint NOT NULL,
    output_credits_per_1m bigint NOT NULL,
    cache_write_credits_per_1m bigint,
    cache_read_credits_per_1m bigint,
    provider character varying(32) NOT NULL,
    provider_model character varying(64),
    context_window integer DEFAULT 128000 NOT NULL,
    max_output_tokens integer DEFAULT 4096 NOT NULL,
    supports_streaming boolean DEFAULT true NOT NULL,
    supports_tools boolean DEFAULT true NOT NULL,
    supports_vision boolean DEFAULT false NOT NULL,
    supports_caching boolean DEFAULT false NOT NULL,
    tier character varying(16) NOT NULL,
    daily_free_quota_credits bigint DEFAULT 0,
    requires_plan character varying(32)[],
    min_credits_per_request bigint DEFAULT 0,
    active boolean DEFAULT true NOT NULL,
    deprecated boolean DEFAULT false NOT NULL,
    replacement_model character varying(64),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    notes text,
    CONSTRAINT cache_write_gte_read CHECK (((cache_write_credits_per_1m IS NULL) OR (cache_read_credits_per_1m IS NULL) OR (cache_write_credits_per_1m >= cache_read_credits_per_1m))),
    CONSTRAINT model_pricing_tier_check CHECK (((tier)::text = ANY (ARRAY[('free'::character varying)::text, ('basic'::character varying)::text, ('standard'::character varying)::text, ('premium'::character varying)::text, ('enterprise'::character varying)::text]))),
    CONSTRAINT positive_input_price CHECK ((input_credits_per_1m >= 0)),
    CONSTRAINT positive_output_price CHECK ((output_credits_per_1m >= 0))
);

