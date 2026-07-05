--
-- Name: credential_model_bindings cmb_unique_credential_model; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_bindings
    ADD CONSTRAINT cmb_unique_credential_model UNIQUE (credential_id, provider_model_id);

