--
-- Name: severity_action_matrix update_severity_action_matrix_modtime; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_severity_action_matrix_modtime BEFORE UPDATE ON public.severity_action_matrix FOR EACH ROW EXECUTE FUNCTION public.update_modified_column();

