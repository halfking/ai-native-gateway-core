--
-- Name: idx_tool_usage_stats_date; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_usage_stats_date ON public.tool_usage_stats USING btree (usage_date DESC);

