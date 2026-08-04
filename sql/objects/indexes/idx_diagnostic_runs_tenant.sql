--
-- Name: idx_diagnostic_runs_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_diagnostic_runs_tenant ON public.diagnostic_runs USING btree (tenant_id, created_at DESC);

