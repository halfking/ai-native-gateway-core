--
-- Name: idx_model_integrity_events_cred_model_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_integrity_events_cred_model_type ON public.model_integrity_events USING btree (credential_id, raw_model_name, anomaly_type, ts DESC);

