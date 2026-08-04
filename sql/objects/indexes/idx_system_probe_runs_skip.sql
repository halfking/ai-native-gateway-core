--
-- Name: idx_system_probe_runs_skip; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_system_probe_runs_skip ON ONLY public.system_probe_runs USING btree (skip_reason) WHERE (skip_reason IS NOT NULL);

