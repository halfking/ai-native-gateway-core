--
-- Name: idx_request_logs_stream_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_stream_ts ON ONLY public.request_logs USING btree (stream_interrupted, ts DESC);

