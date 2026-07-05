--
-- Name: idx_request_logs_identity_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_identity_hash ON ONLY public.request_logs USING btree (identity_hash, ts DESC) WHERE (identity_hash IS NOT NULL);

