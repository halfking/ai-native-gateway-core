--
-- Name: session_module_executions session_module_executions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_module_executions
    ADD CONSTRAINT session_module_executions_pkey PRIMARY KEY (execution_id, created_at);

