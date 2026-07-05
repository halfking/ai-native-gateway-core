--
-- Name: model_offers model_offers_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER model_offers_update INSTEAD OF UPDATE ON public.model_offers FOR EACH ROW EXECUTE FUNCTION public.model_offers_update_trigger();

