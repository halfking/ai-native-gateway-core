--
-- Name: idx_output_compliance_review_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_output_compliance_review_status ON public.output_compliance_review_queue USING btree (tenant_id, status, created_at DESC);

