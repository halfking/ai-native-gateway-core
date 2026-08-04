--
-- Name: prompt_injection_detections prompt_injection_detections_super_admin; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY prompt_injection_detections_super_admin ON public.prompt_injection_detections USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

