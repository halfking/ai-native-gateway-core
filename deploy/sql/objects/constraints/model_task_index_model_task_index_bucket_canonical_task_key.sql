--
-- Name: model_task_index model_task_index_bucket_canonical_task_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_task_index
    ADD CONSTRAINT model_task_index_bucket_canonical_task_key UNIQUE (bucket, canonical_id, task_type);

