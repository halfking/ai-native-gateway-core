--
-- Name: idx_session_audit_records_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_audit_records_session ON public.session_audit_records USING btree (session_id, created_at DESC);

