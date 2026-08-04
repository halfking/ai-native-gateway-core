--
-- Name: idx_diagnostic_runs_tenant_started; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_diagnostic_runs_tenant_started ON public.diagnostic_runs USING btree (tenant_id, started_at DESC);

