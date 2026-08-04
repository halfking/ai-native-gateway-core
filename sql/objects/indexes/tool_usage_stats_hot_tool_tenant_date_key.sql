--
-- Name: tool_usage_stats_hot_tool_tenant_date_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX tool_usage_stats_hot_tool_tenant_date_key ON public.tool_usage_stats_hot USING btree (tool_id, tenant_id, usage_date);

