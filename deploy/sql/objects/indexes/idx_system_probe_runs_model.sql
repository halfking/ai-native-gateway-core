--
-- Name: idx_system_probe_runs_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_system_probe_runs_model ON ONLY public.system_probe_runs USING btree (raw_model, created_at DESC);

