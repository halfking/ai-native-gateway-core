--
-- Name: canary_tokens canary_tokens_tenant; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY canary_tokens_tenant ON public.canary_tokens USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));

