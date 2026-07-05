--
-- Name: credentials credentials_unique_provider_label; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT credentials_unique_provider_label UNIQUE (provider_id, tenant_id, label);

