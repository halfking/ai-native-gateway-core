--
-- Name: idx_session_audit_records_tenant_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_audit_records_tenant_created ON public.session_audit_records USING btree (tenant_id, created_at DESC);

