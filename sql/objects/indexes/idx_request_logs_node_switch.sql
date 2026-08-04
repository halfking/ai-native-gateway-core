--
-- Name: idx_request_logs_node_switch; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_node_switch ON ONLY public.request_logs USING btree (node_switch_count, ts DESC) WHERE (node_switch_count > 0);

