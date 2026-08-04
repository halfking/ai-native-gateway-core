--
-- Name: model_name_mapping model_name_mapping_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER model_name_mapping_updated_at BEFORE UPDATE ON public.model_name_mapping FOR EACH ROW EXECUTE FUNCTION public.model_name_mapping_updated_at();

