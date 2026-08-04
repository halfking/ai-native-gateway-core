--
-- Name: idx_request_logs_upstream_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_upstream_status ON ONLY public.request_logs USING btree (upstream_status_code, ts DESC) WHERE (upstream_status_code IS NOT NULL);

