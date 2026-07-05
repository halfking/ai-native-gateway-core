--
-- Name: tenant_credit_wallets tenant_isolation_tenant_credit_wallets; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tenant_credit_wallets ON public.tenant_credit_wallets USING (((tenant_id)::text = public.get_current_tenant()));

