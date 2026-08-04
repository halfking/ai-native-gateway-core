--
-- Name: credential_probe_configs credential_probe_configs_credential_id_probe_model_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_probe_configs
    ADD CONSTRAINT credential_probe_configs_credential_id_probe_model_key UNIQUE (credential_id, probe_model);

