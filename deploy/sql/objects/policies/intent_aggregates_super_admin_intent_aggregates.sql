--
-- Name: intent_aggregates super_admin_intent_aggregates; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY super_admin_intent_aggregates ON public.intent_aggregates USING ((current_setting('app.is_super_admin'::text, true) = 'true'::text));

