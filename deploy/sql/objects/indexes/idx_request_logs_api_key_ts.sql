--
-- Name: idx_request_logs_api_key_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_api_key_ts ON ONLY public.request_logs USING btree (api_key_id, ts DESC);

