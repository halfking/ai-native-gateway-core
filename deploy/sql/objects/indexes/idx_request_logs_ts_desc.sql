--
-- Name: idx_request_logs_ts_desc; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_ts_desc ON ONLY public.request_logs USING btree (ts DESC);

