--
-- Name: approval_rules approval_rules_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER approval_rules_updated_at BEFORE UPDATE ON public.approval_rules FOR EACH ROW EXECUTE FUNCTION public.update_approval_updated_at();

