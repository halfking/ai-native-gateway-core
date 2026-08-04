--
-- Name: idx_request_logs_hot_credential_model_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_hot_credential_model_ts ON public.request_logs_hot USING btree (credential_id, lower(COALESCE(outbound_model, client_model)), ts DESC);

