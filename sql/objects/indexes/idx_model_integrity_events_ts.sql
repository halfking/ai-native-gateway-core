--
-- Name: idx_model_integrity_events_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_integrity_events_ts ON public.model_integrity_events USING btree (ts DESC);

