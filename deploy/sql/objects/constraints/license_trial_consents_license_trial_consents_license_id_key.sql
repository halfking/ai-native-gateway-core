--
-- Name: license_trial_consents license_trial_consents_license_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_trial_consents
    ADD CONSTRAINT license_trial_consents_license_id_key UNIQUE (license_id);

