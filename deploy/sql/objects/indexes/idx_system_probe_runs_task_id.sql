--
-- Name: idx_system_probe_runs_task_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_system_probe_runs_task_id ON ONLY public.system_probe_runs USING btree (task_id);

