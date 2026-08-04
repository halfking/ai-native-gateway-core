--
-- Name: idx_request_logs_client_ip; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_client_ip ON ONLY public.request_logs USING btree (client_ip);

