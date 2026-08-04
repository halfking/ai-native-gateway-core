--
-- Name: idx_system_probe_runs_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_system_probe_runs_status ON ONLY public.system_probe_runs USING btree (status, created_at DESC);

