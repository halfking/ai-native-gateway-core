--
-- Name: provider_quality_configs provider_quality_configs_provider_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_quality_configs
    ADD CONSTRAINT provider_quality_configs_provider_id_key UNIQUE (provider_id);

