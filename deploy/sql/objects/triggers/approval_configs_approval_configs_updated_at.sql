--
-- Name: approval_configs approval_configs_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER approval_configs_updated_at BEFORE UPDATE ON public.approval_configs FOR EACH ROW EXECUTE FUNCTION public.update_approval_updated_at();

