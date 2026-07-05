--
-- Name: idx_tool_usage_stats_tool_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_usage_stats_tool_id ON public.tool_usage_stats USING btree (tool_id);

