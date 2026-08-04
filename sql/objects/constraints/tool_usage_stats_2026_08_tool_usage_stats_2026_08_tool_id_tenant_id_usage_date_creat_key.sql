--
-- Name: tool_usage_stats_2026_08 tool_usage_stats_2026_08_tool_id_tenant_id_usage_date_creat_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats_2026_08
    ADD CONSTRAINT tool_usage_stats_2026_08_tool_id_tenant_id_usage_date_creat_key UNIQUE (tool_id, tenant_id, usage_date, created_at);

