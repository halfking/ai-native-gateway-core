--
-- Name: injection_attack_vectors attack_vectors_super_admin; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY attack_vectors_super_admin ON public.injection_attack_vectors USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

