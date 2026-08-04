--
-- Name: idx_request_logs_cached_response; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_cached_response ON ONLY public.request_logs USING btree (cached_response_id) WHERE (cached_response_id IS NOT NULL);

