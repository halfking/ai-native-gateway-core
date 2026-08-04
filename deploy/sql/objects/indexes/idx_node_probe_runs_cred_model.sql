--
-- Name: idx_node_probe_runs_cred_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_node_probe_runs_cred_model ON public.node_probe_runs USING btree (credential_id, raw_model_name, started_at DESC);

