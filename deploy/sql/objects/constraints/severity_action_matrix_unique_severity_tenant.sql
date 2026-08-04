--
-- Name: severity_action_matrix unique_severity_tenant; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.severity_action_matrix
    ADD CONSTRAINT unique_severity_tenant UNIQUE (tenant_id, severity_level);

