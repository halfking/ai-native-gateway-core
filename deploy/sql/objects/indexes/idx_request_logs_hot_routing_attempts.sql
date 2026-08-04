--
-- Name: idx_request_logs_hot_routing_attempts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_hot_routing_attempts ON public.request_logs_hot USING gin (routing_attempts) WHERE (routing_attempts IS NOT NULL);

