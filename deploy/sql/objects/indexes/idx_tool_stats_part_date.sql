--
-- Name: idx_tool_stats_part_date; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_stats_part_date ON ONLY public.tool_usage_stats USING btree (usage_date);

