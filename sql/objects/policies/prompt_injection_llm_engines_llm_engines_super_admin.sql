--
-- Name: prompt_injection_llm_engines llm_engines_super_admin; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY llm_engines_super_admin ON public.prompt_injection_llm_engines USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

