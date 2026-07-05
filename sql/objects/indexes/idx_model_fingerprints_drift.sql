--
-- Name: idx_model_fingerprints_drift; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_fingerprints_drift ON public.model_fingerprints USING btree (drift_detected) WHERE (drift_detected = true);

