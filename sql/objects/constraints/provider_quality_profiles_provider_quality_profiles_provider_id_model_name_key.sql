--
-- Name: provider_quality_profiles provider_quality_profiles_provider_id_model_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_quality_profiles
    ADD CONSTRAINT provider_quality_profiles_provider_id_model_name_key UNIQUE (provider_id, model_name);

