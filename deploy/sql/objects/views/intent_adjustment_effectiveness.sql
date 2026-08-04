--
-- Name: intent_adjustment_effectiveness; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.intent_adjustment_effectiveness AS
 SELECT tenant_id,
    adjustment_type,
    target_intent,
    status,
    count(*) AS adjustment_count,
    avg(effectiveness_score) AS avg_effectiveness,
    avg((after_accuracy - before_accuracy)) AS avg_accuracy_improvement,
    sum(
        CASE
            WHEN (status = 'active'::text) THEN 1
            ELSE 0
        END) AS active_count,
    sum(
        CASE
            WHEN (status = 'rolled_back'::text) THEN 1
            ELSE 0
        END) AS rollback_count
   FROM public.intent_analysis_adjustments
  WHERE (effectiveness_score IS NOT NULL)
  GROUP BY tenant_id, adjustment_type, target_intent, status;


--
-- Name: VIEW intent_adjustment_effectiveness; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.intent_adjustment_effectiveness IS '配置调整效果分析 — 统计各类调整的平均效果、准确率提升和回滚率';

