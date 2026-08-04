--
-- Name: idx_stage_events_stage_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stage_events_stage_status ON public.request_stage_events USING btree (stage, status, event_timestamp DESC) WHERE (status = ANY (ARRAY['failed'::text, 'timeout'::text]));

