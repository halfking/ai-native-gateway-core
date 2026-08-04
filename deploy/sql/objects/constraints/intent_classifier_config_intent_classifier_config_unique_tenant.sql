--
-- Name: intent_classifier_config intent_classifier_config_unique_tenant; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.intent_classifier_config
    ADD CONSTRAINT intent_classifier_config_unique_tenant UNIQUE (tenant_id);

