--
-- Name: update_intent_feedback_correctness(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.update_intent_feedback_correctness() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    -- 当actual_intent被设置时，自动计算is_correct
    IF NEW.actual_intent IS NOT NULL AND OLD.actual_intent IS NULL THEN
        NEW.is_correct := (NEW.predicted_intent = NEW.actual_intent);
        NEW.annotated_at := NOW();
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: FUNCTION update_intent_feedback_correctness(); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.update_intent_feedback_correctness() IS '触发器函数：当actual_intent被标注时，自动计算is_correct并设置annotated_at';

