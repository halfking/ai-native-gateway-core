--
-- Name: intent_aggregates tenant_isolation_intent_aggregates; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_intent_aggregates ON public.intent_aggregates USING ((tenant_id = public.get_current_tenant()));

