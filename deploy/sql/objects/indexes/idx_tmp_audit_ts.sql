--
-- Name: idx_tmp_audit_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tmp_audit_ts ON public.tenant_model_policies_audit USING btree (ts DESC);

