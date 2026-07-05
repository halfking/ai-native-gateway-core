--
-- Name: tenant_subscriptions tenant_isolation_tenant_subscriptions; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tenant_subscriptions ON public.tenant_subscriptions USING (((tenant_id)::text = public.get_current_tenant()));

