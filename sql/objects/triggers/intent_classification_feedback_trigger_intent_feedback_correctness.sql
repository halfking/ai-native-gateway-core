--
-- Name: intent_classification_feedback trigger_intent_feedback_correctness; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trigger_intent_feedback_correctness BEFORE UPDATE ON public.intent_classification_feedback FOR EACH ROW EXECUTE FUNCTION public.update_intent_feedback_correctness();

