--
-- Name: idx_response_format_anomalies_bridge; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_response_format_anomalies_bridge ON public.response_format_anomalies USING btree (resolved, detected_at, anomaly_type, severity) WHERE (NOT resolved);

