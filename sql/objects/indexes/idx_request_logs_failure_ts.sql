--
-- Name: idx_request_logs_failure_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_failure_ts ON ONLY public.request_logs USING btree (failure_stage, failure_detail_code, ts DESC);

