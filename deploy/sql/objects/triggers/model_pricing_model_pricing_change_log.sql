--
-- Name: model_pricing model_pricing_change_log; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER model_pricing_change_log AFTER UPDATE ON public.model_pricing FOR EACH ROW EXECUTE FUNCTION public.log_model_pricing_change();

