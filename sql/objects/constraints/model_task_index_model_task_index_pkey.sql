--
-- Name: model_task_index model_task_index_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_task_index
    ADD CONSTRAINT model_task_index_pkey PRIMARY KEY (bucket, canonical_id, task_type);

