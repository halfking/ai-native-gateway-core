--
-- Name: tool_usage_stats uk_tool_usage_stats; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats
    ADD CONSTRAINT uk_tool_usage_stats UNIQUE (tool_id, tenant_id, usage_date);

