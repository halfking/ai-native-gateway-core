--
-- Name: session_summaries session_summaries_tenant_isolation; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY session_summaries_tenant_isolation ON public.session_summaries USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));

