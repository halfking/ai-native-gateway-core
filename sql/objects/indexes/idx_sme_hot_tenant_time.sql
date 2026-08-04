--
-- Name: idx_sme_hot_tenant_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sme_hot_tenant_time ON public.session_module_executions_hot USING btree (tenant_id, created_at DESC);

