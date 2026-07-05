--
-- Name: idx_tool_call_events_tool_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_call_events_tool_id ON public.tool_call_events USING btree (tool_id, called_at DESC);

