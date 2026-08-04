--
-- Name: idx_system_probe_runs_automaticity; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_system_probe_runs_automaticity ON ONLY public.system_probe_runs USING btree (automaticity, created_at DESC);

