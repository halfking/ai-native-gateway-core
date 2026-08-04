--
-- Name: session_intent_evolution; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_intent_evolution (
    id bigint NOT NULL,
    session_id text NOT NULL,
    tenant_id text NOT NULL,
    request_id text NOT NULL,
    turn_number integer NOT NULL,
    intent_candidates jsonb DEFAULT '[]'::jsonb NOT NULL,
    primary_intent text NOT NULL,
    primary_confidence double precision NOT NULL,
    previous_primary_intent text,
    intent_drift_score double precision,
    is_intent_changed boolean DEFAULT false,
    classifier_version text DEFAULT 'v2_pattern'::text NOT NULL,
    classification_latency_ms integer,
    user_content text,
    user_content_hash text,
    context_length integer DEFAULT 0,
    has_images boolean DEFAULT false,
    tool_count integer DEFAULT 0,
    classified_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT session_intent_evolution_intent_drift_score_check CHECK (((intent_drift_score >= (0)::double precision) AND (intent_drift_score <= (1)::double precision))),
    CONSTRAINT session_intent_evolution_primary_confidence_check CHECK (((primary_confidence >= (0)::double precision) AND (primary_confidence <= (1)::double precision)))
);


--
-- Name: TABLE session_intent_evolution; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_intent_evolution IS '多轮意图分析 — 记录每轮对话的意图判断和演化轨迹，支持意图漂移检测';

