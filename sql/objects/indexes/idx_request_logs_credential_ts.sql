--
-- Name: idx_request_logs_credential_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_credential_ts ON ONLY public.request_logs USING btree (credential_id, ts DESC);

