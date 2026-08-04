--
-- Name: idx_output_audit_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_output_audit_session ON public.output_compliance_audit USING btree (session_key);

