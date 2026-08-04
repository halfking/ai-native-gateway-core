--
-- Name: idx_request_logs_hot_api_key_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_hot_api_key_ts ON public.request_logs_hot USING btree (api_key_id, ts DESC) WHERE (api_key_id IS NOT NULL);

