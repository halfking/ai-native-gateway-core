--
-- Name: idx_request_logs_request_id_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_request_id_ts ON ONLY public.request_logs USING btree (request_id, ts DESC);

