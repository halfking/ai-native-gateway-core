--
-- Name: idx_model_discovery_runs_tenant_started; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_discovery_runs_tenant_started ON public.model_discovery_runs USING btree (tenant_id, started_at DESC);

