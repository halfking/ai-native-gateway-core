--
-- Name: idx_system_probe_runs_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_system_probe_runs_provider ON ONLY public.system_probe_runs USING btree (provider_id, created_at DESC);

