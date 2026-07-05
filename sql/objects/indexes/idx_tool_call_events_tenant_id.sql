--
-- Name: idx_tool_call_events_tenant_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_call_events_tenant_id ON public.tool_call_events USING btree (tenant_id, called_at DESC);

