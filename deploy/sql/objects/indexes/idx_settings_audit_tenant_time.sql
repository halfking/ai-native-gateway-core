--
-- Name: idx_settings_audit_tenant_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_audit_tenant_time ON public.settings_audit USING btree (tenant_id, created_at DESC);

