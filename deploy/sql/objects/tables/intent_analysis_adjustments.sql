--
-- Name: intent_analysis_adjustments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.intent_analysis_adjustments (
    id bigint NOT NULL,
    tenant_id text NOT NULL,
    adjustment_type text NOT NULL,
    target_intent text,
    adjustment_detail jsonb NOT NULL,
    reason text,
    triggered_by text DEFAULT 'manual'::text NOT NULL,
    operator_id text,
    effectiveness_score double precision,
    evaluation_sample_size integer,
    before_accuracy double precision,
    after_accuracy double precision,
    status text DEFAULT 'active'::text NOT NULL,
    rollback_reason text,
    superseded_by bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    evaluated_at timestamp with time zone,
    rolled_back_at timestamp with time zone,
    CONSTRAINT intent_analysis_adjustments_after_accuracy_check CHECK (((after_accuracy >= (0)::double precision) AND (after_accuracy <= (1)::double precision))),
    CONSTRAINT intent_analysis_adjustments_before_accuracy_check CHECK (((before_accuracy >= (0)::double precision) AND (before_accuracy <= (1)::double precision))),
    CONSTRAINT intent_analysis_adjustments_check CHECK ((((status = 'active'::text) AND (rollback_reason IS NULL) AND (rolled_back_at IS NULL)) OR ((status = 'rolled_back'::text) AND (rollback_reason IS NOT NULL) AND (rolled_back_at IS NOT NULL)) OR ((status = 'superseded'::text) AND (superseded_by IS NOT NULL)))),
    CONSTRAINT intent_analysis_adjustments_effectiveness_score_check CHECK (((effectiveness_score >= (0)::double precision) AND (effectiveness_score <= (1)::double precision)))
);


--
-- Name: TABLE intent_analysis_adjustments; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.intent_analysis_adjustments IS '意图分析调整记录 — 追踪配置变更历史、原因和效果评估，支持版本管理和回滚';

