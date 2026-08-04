--
-- Name: idx_request_logs_client_model_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_client_model_hash ON ONLY public.request_logs USING hash (client_model);

