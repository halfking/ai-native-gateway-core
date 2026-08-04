--
-- Name: idx_stage_events_upstream_failure; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stage_events_upstream_failure ON public.request_stage_events USING btree (stage, http_status, event_timestamp DESC) WHERE ((stage = 'upstream_request'::text) AND (http_status >= 500));

