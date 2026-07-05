--
-- Name: tool_registry tool_registry_tool_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_registry
    ADD CONSTRAINT tool_registry_tool_name_key UNIQUE (tool_name);

