--
-- Name: idx_request_logs_agent_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_agent_type ON ONLY public.request_logs USING btree (agent_type);

