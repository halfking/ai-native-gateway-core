--
-- Name: idx_request_logs_timeout_analysis; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_timeout_analysis ON ONLY public.request_logs USING btree (effective_timeout_seconds, latency_ms) WHERE (effective_timeout_seconds IS NOT NULL);

