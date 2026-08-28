--
-- Name: credential_model_capabilities credential_model_capabilities_binding_capability_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_capabilities
    ADD CONSTRAINT credential_model_capabilities_binding_capability_key UNIQUE (credential_model_binding_id, capability);
