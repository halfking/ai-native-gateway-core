--
-- Name: idx_tool_stats_part_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_stats_part_created ON ONLY public.tool_usage_stats USING btree (created_at);

