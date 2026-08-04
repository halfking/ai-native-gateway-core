--
-- Name: idx_tool_call_events_called_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_call_events_called_at ON public.tool_call_events USING btree (called_at DESC);

