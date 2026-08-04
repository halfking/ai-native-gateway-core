--
-- Name: provider_profile_daily provider_profile_daily_credential_id_profile_date_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_profile_daily
    ADD CONSTRAINT provider_profile_daily_credential_id_profile_date_key UNIQUE (credential_id, profile_date);

