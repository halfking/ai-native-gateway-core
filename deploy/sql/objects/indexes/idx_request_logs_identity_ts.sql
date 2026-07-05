--
-- Name: idx_request_logs_identity_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_identity_ts ON ONLY public.request_logs USING btree (identity_hash, ts DESC);

