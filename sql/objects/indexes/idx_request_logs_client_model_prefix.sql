--
-- Name: idx_request_logs_client_model_prefix; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_client_model_prefix ON ONLY public.request_logs USING btree (client_model text_pattern_ops);

