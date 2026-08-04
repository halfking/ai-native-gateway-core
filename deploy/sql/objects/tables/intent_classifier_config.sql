--
-- Name: intent_classifier_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.intent_classifier_config (
    id integer NOT NULL,
    tenant_id text,
    strategy text DEFAULT 'pattern_layered'::text NOT NULL,
    enabled_layers jsonb DEFAULT '{"hard_rules": true, "llm_fallback": false, "keyword_score": true, "pattern_match": true}'::jsonb NOT NULL,
    keywords_config jsonb DEFAULT '{}'::jsonb NOT NULL,
    patterns_config jsonb DEFAULT '{}'::jsonb NOT NULL,
    confidence_thresholds jsonb DEFAULT '{"low": 0.40, "high": 0.80, "medium": 0.60}'::jsonb NOT NULL,
    drift_threshold double precision DEFAULT 0.3 NOT NULL,
    multi_turn_memory integer DEFAULT 5 NOT NULL,
    llm_fallback_enabled boolean DEFAULT false NOT NULL,
    llm_model text DEFAULT 'gpt-4o-mini'::text,
    llm_confidence_threshold double precision DEFAULT 0.50,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT intent_classifier_config_drift_threshold_check CHECK (((drift_threshold >= (0)::double precision) AND (drift_threshold <= (1)::double precision))),
    CONSTRAINT intent_classifier_config_llm_confidence_threshold_check CHECK (((llm_confidence_threshold >= (0)::double precision) AND (llm_confidence_threshold <= (1)::double precision))),
    CONSTRAINT intent_classifier_config_multi_turn_memory_check CHECK (((multi_turn_memory > 0) AND (multi_turn_memory <= 20)))
);


--
-- Name: TABLE intent_classifier_config; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.intent_classifier_config IS '意图分类器配置 — 租户级可配置的分类策略、关键词、模式和阈值，支持热更新';

