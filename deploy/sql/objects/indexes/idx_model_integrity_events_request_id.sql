--
-- Name: idx_model_integrity_events_request_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_integrity_events_request_id ON public.model_integrity_events USING btree (request_id) WHERE (request_id IS NOT NULL);

