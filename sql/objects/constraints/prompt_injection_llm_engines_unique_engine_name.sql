--
-- Name: prompt_injection_llm_engines unique_engine_name; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.prompt_injection_llm_engines
    ADD CONSTRAINT unique_engine_name UNIQUE (tenant_id, engine_name);

