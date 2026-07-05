--
-- Name: credit_ledger tenant_isolation_credit_ledger; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_credit_ledger ON public.credit_ledger USING (((tenant_id)::text = public.get_current_tenant()));

