--
-- Name: local_runtimes local_runtimes_host_code_runtime_type_base_url_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.local_runtimes
    ADD CONSTRAINT local_runtimes_host_code_runtime_type_base_url_key UNIQUE (host_code, runtime_type, base_url);

