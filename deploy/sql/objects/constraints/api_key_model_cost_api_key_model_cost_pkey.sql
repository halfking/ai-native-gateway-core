--
-- Name: api_key_model_cost api_key_model_cost_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_key_model_cost
    ADD CONSTRAINT api_key_model_cost_pkey PRIMARY KEY (bucket, api_key_id, raw_model);

