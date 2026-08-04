--
-- Name: idx_system_probe_runs_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_system_probe_runs_credential ON ONLY public.system_probe_runs USING btree (credential_id, created_at DESC);

