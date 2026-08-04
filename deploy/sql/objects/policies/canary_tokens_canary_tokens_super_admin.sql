--
-- Name: canary_tokens canary_tokens_super_admin; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY canary_tokens_super_admin ON public.canary_tokens USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

