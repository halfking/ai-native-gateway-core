--
-- Name: intent_classification_feedback; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.intent_classification_feedback (
    id bigint NOT NULL,
    session_id text NOT NULL,
    request_id text NOT NULL,
    tenant_id text NOT NULL,
    predicted_intent text NOT NULL,
    predicted_confidence double precision NOT NULL,
    actual_intent text,
    is_correct boolean,
    annotator_id text,
    annotated_at timestamp with time zone,
    annotation_notes text,
    user_accepted_model boolean,
    user_switched_to_model text,
    user_retry_count integer DEFAULT 0,
    session_duration_sec integer,
    user_satisfaction_score integer,
    user_content_hash text,
    classification_context jsonb,
    evolution_id bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT intent_classification_feedback_predicted_confidence_check CHECK (((predicted_confidence >= (0)::double precision) AND (predicted_confidence <= (1)::double precision))),
    CONSTRAINT intent_classification_feedback_session_duration_sec_check CHECK ((session_duration_sec >= 0)),
    CONSTRAINT intent_classification_feedback_user_retry_count_check CHECK ((user_retry_count >= 0)),
    CONSTRAINT intent_classification_feedback_user_satisfaction_score_check CHECK (((user_satisfaction_score >= 1) AND (user_satisfaction_score <= 5)))
);


--
-- Name: TABLE intent_classification_feedback; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.intent_classification_feedback IS '意图分类反馈 — 收集人工标注和用户行为反馈，用于评估准确率和自动优化';

