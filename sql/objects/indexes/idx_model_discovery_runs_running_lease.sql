--
-- Name: idx_model_discovery_runs_running_lease; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_discovery_runs_running_lease ON public.model_discovery_runs USING btree (tenant_id, lease_expires_at) WHERE (status = 'running'::text);

