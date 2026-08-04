--
-- Name: provider_profile_whitelist provider_profile_whitelist_provider_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_profile_whitelist
    ADD CONSTRAINT provider_profile_whitelist_provider_id_key UNIQUE (provider_id);

