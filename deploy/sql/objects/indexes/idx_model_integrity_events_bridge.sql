--
-- Name: idx_model_integrity_events_bridge; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_integrity_events_bridge ON public.model_integrity_events USING btree (resolved, ts, anomaly_type, severity) WHERE (resolved = false);

