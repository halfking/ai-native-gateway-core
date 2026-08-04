--
-- Name: model_name_mapping model_name_mapping_raw_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_name_mapping
    ADD CONSTRAINT model_name_mapping_raw_unique UNIQUE (raw_model_name);

