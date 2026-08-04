--
-- Name: idx_session_audit_records_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_audit_records_status ON public.session_audit_records USING btree (status, created_at DESC) WHERE (status = 'need_approval'::text);

