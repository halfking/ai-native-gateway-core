--
-- Name: idx_node_probe_runs_api_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_node_probe_runs_api_model ON public.node_probe_runs USING btree (api_model, started_at DESC);

