--
-- Name: tool_usage_stats_2026_07 tool_usage_stats_2026_07_tool_id_tenant_id_usage_date_creat_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats_2026_07
    ADD CONSTRAINT tool_usage_stats_2026_07_tool_id_tenant_id_usage_date_creat_key UNIQUE (tool_id, tenant_id, usage_date, created_at);

