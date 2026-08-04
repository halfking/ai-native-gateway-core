--
-- Name: license_modules license_modules_license_id_module_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_modules
    ADD CONSTRAINT license_modules_license_id_module_key_key UNIQUE (license_id, module_key);

