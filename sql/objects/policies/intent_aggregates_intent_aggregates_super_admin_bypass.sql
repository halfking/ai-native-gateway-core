--
-- Name: intent_aggregates intent_aggregates_super_admin_bypass; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY intent_aggregates_super_admin_bypass ON public.intent_aggregates USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

