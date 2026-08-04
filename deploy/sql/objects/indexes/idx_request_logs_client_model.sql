--
-- Name: idx_request_logs_client_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_client_model ON ONLY public.request_logs USING btree (client_model);

