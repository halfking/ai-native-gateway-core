--
-- Name: idx_request_logs_client_model_lower; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_client_model_lower ON ONLY public.request_logs USING btree (lower(client_model));

