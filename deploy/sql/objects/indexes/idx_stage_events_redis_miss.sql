--
-- Name: idx_stage_events_redis_miss; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stage_events_redis_miss ON public.request_stage_events USING btree (stage, event_timestamp DESC) WHERE (redis_hit = false);

