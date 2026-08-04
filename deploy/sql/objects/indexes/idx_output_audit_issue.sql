--
-- Name: idx_output_audit_issue; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_output_audit_issue ON public.output_compliance_audit USING btree (tenant_id, issue_type, severity DESC);

