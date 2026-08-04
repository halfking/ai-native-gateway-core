--
-- Name: idx_session_module_executions_2026_08_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_module_executions_2026_08_tenant ON public.session_module_executions_2026_08 USING btree (tenant_id, created_at DESC);

