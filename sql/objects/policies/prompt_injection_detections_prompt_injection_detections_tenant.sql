--
-- Name: prompt_injection_detections prompt_injection_detections_tenant; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY prompt_injection_detections_tenant ON public.prompt_injection_detections USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));

