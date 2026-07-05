--
-- Name: idx_tool_usage_stats_tenant_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_usage_stats_tenant_id ON public.tool_usage_stats USING btree (tenant_id);

