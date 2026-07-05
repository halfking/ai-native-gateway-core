--
-- Name: tenant_model_policies tenant_isolation_tmp; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tmp ON public.tenant_model_policies USING (((tenant_id)::text = public.get_current_tenant()));

