--
-- Name: session_module_executions_hot uk_sme_hot_session_module_batch; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_module_executions_hot
    ADD CONSTRAINT uk_sme_hot_session_module_batch UNIQUE (gw_session_id, module_name, batch_key, started_at);

