--
-- Name: intent_classification_metrics; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.intent_classification_metrics AS
 SELECT tenant_id,
    date_trunc('day'::text, created_at) AS date,
    predicted_intent,
    count(*) AS total_classifications,
    sum(
        CASE
            WHEN is_correct THEN 1
            ELSE 0
        END) AS correct_count,
    avg(
        CASE
            WHEN is_correct THEN 1.0
            ELSE 0.0
        END) AS accuracy,
    avg(predicted_confidence) AS avg_confidence,
    stddev(predicted_confidence) AS confidence_stddev,
    avg(
        CASE
            WHEN user_accepted_model THEN 1.0
            ELSE 0.0
        END) AS model_acceptance_rate,
    avg(user_retry_count) AS avg_retry_count,
    avg(session_duration_sec) AS avg_session_duration,
    avg(user_satisfaction_score) AS avg_satisfaction_score
   FROM public.intent_classification_feedback
  WHERE (annotated_at IS NOT NULL)
  GROUP BY tenant_id, (date_trunc('day'::text, created_at)), predicted_intent;


--
-- Name: VIEW intent_classification_metrics; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.intent_classification_metrics IS '意图分类效果指标 — 按天、按租户、按意图类型统计准确率、置信度和用户行为';

