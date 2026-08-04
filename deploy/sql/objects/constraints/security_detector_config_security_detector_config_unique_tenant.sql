--
-- Name: security_detector_config security_detector_config_unique_tenant; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.security_detector_config
    ADD CONSTRAINT security_detector_config_unique_tenant UNIQUE (tenant_id, config_name);

