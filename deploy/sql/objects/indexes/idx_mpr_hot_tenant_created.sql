--
-- Name: idx_mpr_hot_tenant_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mpr_hot_tenant_created ON public.model_probe_runs_hot USING btree (tenant_id, created_at DESC);

