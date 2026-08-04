--
-- Name: output_compliance_custom_keywords update_output_compliance_custom_keywords_modtime; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_output_compliance_custom_keywords_modtime BEFORE UPDATE ON public.output_compliance_custom_keywords FOR EACH ROW EXECUTE FUNCTION public.update_output_compliance_modified_column();

