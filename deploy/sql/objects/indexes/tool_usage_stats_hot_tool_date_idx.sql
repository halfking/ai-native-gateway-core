--
-- Name: tool_usage_stats_hot_tool_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tool_usage_stats_hot_tool_date_idx ON public.tool_usage_stats_hot USING btree (tool_id, usage_date DESC);

