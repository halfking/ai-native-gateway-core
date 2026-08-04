--
-- Name: settings_kv settings_kv_key_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settings_kv
    ADD CONSTRAINT settings_kv_key_unique UNIQUE (key);

