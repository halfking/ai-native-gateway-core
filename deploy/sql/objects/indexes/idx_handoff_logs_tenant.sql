--
-- Name: idx_handoff_logs_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_handoff_logs_tenant ON public.handoff_logs USING btree (tenant_id, created_at DESC);

