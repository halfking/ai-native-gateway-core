--
-- Name: idx_output_audit_tenant_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_output_audit_tenant_time ON public.output_compliance_audit USING btree (tenant_id, detected_at DESC);

