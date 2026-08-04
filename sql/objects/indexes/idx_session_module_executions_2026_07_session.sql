--
-- Name: idx_session_module_executions_2026_07_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_module_executions_2026_07_session ON public.session_module_executions_2026_07 USING btree (gw_session_id, module_name);

