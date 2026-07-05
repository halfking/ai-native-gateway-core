--
-- Name: idx_request_logs_usage_source_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_usage_source_ts ON ONLY public.request_logs USING btree (usage_source, ts DESC) WHERE (usage_source = 'estimated'::text);

