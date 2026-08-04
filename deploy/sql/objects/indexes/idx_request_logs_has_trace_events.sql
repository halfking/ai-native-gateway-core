--
-- Name: idx_request_logs_has_trace_events; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_has_trace_events ON ONLY public.request_logs USING btree (request_id) WHERE (trace_events IS NOT NULL);

