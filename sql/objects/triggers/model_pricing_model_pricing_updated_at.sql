--
-- Name: model_pricing model_pricing_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER model_pricing_updated_at BEFORE UPDATE ON public.model_pricing FOR EACH ROW EXECUTE FUNCTION public.update_model_pricing_updated_at();

