--
-- Name: tool_usage_stats tool_usage_stats_partitioned_tool_id_tenant_id_usage_date_c_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats
    ADD CONSTRAINT tool_usage_stats_partitioned_tool_id_tenant_id_usage_date_c_key UNIQUE (tool_id, tenant_id, usage_date, created_at);

