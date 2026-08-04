--
-- Name: idx_node_probe_runs_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_node_probe_runs_provider ON public.node_probe_runs USING btree (provider_id, started_at DESC);

