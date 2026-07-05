--
-- Name: idx_credential_probe_model_log_tenant_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_probe_model_log_tenant_time ON public.credential_probe_model_log USING btree (tenant_id, created_at DESC);

