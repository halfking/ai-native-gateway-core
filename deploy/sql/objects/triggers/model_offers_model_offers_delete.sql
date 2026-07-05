--
-- Name: model_offers model_offers_delete; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER model_offers_delete INSTEAD OF DELETE ON public.model_offers FOR EACH ROW EXECUTE FUNCTION public.model_offers_delete_trigger();

