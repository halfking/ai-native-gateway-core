--
-- Name: license_holders license_holders_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_holders
    ADD CONSTRAINT license_holders_email_key UNIQUE (email);

