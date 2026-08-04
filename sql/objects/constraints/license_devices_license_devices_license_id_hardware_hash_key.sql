--
-- Name: license_devices license_devices_license_id_hardware_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_devices
    ADD CONSTRAINT license_devices_license_id_hardware_hash_key UNIQUE (license_id, hardware_hash);

