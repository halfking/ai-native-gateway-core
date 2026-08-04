--
-- Name: idx_output_audit_request; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_output_audit_request ON public.output_compliance_audit USING btree (request_id);

