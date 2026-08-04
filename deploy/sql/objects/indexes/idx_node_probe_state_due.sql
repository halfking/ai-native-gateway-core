--
-- Name: idx_node_probe_state_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_node_probe_state_due ON public.node_probe_state USING btree (next_retry_at) WHERE (paused = false);

