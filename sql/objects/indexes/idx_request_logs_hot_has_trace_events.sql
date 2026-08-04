--
-- Name: idx_request_logs_hot_has_trace_events; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_hot_has_trace_events ON public.request_logs_hot USING btree (request_id) WHERE (trace_events IS NOT NULL);

