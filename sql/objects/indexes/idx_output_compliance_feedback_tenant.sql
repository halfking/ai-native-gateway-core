--
-- Name: idx_output_compliance_feedback_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_output_compliance_feedback_tenant ON public.output_compliance_feedback USING btree (tenant_id, feedback_type, created_at DESC);

