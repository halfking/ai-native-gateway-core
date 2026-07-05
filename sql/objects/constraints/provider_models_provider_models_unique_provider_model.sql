--
-- Name: provider_models provider_models_unique_provider_model; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_models
    ADD CONSTRAINT provider_models_unique_provider_model UNIQUE (provider_id, raw_model_name);

