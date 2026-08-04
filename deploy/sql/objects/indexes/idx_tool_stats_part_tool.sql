--
-- Name: idx_tool_stats_part_tool; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_stats_part_tool ON ONLY public.tool_usage_stats USING btree (tool_id, usage_date);

