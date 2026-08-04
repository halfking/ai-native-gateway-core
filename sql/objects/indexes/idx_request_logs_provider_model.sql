--
-- Name: idx_request_logs_provider_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_provider_model ON ONLY public.request_logs USING btree (provider_model, ts DESC) WHERE (provider_model IS NOT NULL);

