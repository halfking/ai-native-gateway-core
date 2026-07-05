--
-- Name: idx_request_logs_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_ts ON ONLY public.request_logs USING btree (ts DESC);

