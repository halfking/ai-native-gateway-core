--
-- Name: prompt_injection_llm_engines llm_engines_tenant; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY llm_engines_tenant ON public.prompt_injection_llm_engines USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));

