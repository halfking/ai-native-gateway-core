--
-- Name: routing_policy; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_policy (
    id smallint DEFAULT 1 NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    weights_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    sticky_ttl_seconds integer DEFAULT 1800 NOT NULL,
    local_bonus numeric(4,3) DEFAULT 0.000 NOT NULL,
    notes text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    algorithm_version smallint DEFAULT 2,
    retry_per_credential smallint DEFAULT 1,
    tier_fallback_max smallint DEFAULT 4,
    slot_soft_limit_ratio numeric(3,2) DEFAULT 1.00,
    slot_hard_limit_ratio numeric(3,2) DEFAULT 1.50,
    slot_wait_max_ms smallint DEFAULT 200,
    circuit_open_seconds integer DEFAULT 300,
    circuit_failure_threshold smallint DEFAULT 5,
    circuit_max_open_seconds integer DEFAULT 1800,
    featured_models text[] DEFAULT ARRAY['claude-sonnet-5'::text, 'claude-fable-5'::text, 'claude-opus-4-8'::text, 'claude-sonnet-4-6'::text, 'gpt-5.4'::text, 'o5-preview'::text, 'gemini-2.0-flash-exp'::text, 'qwen3-235b'::text, 'minimax-m3'::text, 'deepseek-chat'::text, 'deepseek-coder'::text, 'glm-4v-plus'::text, 'mimo-v2.5-pro'::text, 'moonshot-v1-128k'::text, 'codestral'::text],
    transient_fail_threshold integer DEFAULT 2 NOT NULL,
    stats_window_minutes integer DEFAULT 10,
    stats_update_interval_seconds integer DEFAULT 60,
    scoring_weights_json jsonb DEFAULT '{"price": 10, "session_load": 5, "failure_penalty": 20, "default_price_cny": 5.0, "default_price_usd": 5.0}'::jsonb,
    CONSTRAINT routing_policy_id_check CHECK ((id = 1)),
    CONSTRAINT routing_policy_transient_fail_threshold_check CHECK (((transient_fail_threshold >= 0) AND (transient_fail_threshold <= 10)))
);

