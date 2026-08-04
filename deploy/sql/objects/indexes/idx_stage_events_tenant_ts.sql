--
-- Name: idx_stage_events_tenant_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stage_events_tenant_ts ON public.request_stage_events USING btree (tenant_id, event_timestamp DESC);

