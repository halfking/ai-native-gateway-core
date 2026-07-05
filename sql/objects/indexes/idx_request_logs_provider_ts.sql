--
-- Name: idx_request_logs_provider_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_provider_ts ON ONLY public.request_logs USING btree (provider_id, ts DESC);

