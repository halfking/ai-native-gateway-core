--
-- Name: idx_model_integrity_events_provider_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_integrity_events_provider_type ON public.model_integrity_events USING btree (provider_id, anomaly_type, ts DESC);

