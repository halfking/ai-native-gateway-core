--
-- Name: tenant_model_policies tenant_model_policies_audit_trg; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER tenant_model_policies_audit_trg AFTER INSERT OR DELETE OR UPDATE ON public.tenant_model_policies FOR EACH ROW EXECUTE FUNCTION public.tenant_model_policies_audit_fn();

