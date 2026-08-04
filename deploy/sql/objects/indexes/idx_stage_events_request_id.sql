--
-- Name: idx_stage_events_request_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stage_events_request_id ON public.request_stage_events USING btree (request_id, seq);

