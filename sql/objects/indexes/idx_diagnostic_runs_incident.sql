--
-- Name: idx_diagnostic_runs_incident; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_diagnostic_runs_incident ON public.diagnostic_runs USING btree (incident_id);

