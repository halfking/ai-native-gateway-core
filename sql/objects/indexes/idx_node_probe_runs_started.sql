--
-- Name: idx_node_probe_runs_started; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_node_probe_runs_started ON public.node_probe_runs USING btree (started_at DESC);

