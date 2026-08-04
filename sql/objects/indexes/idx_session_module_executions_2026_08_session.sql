--
-- Name: idx_session_module_executions_2026_08_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_module_executions_2026_08_session ON public.session_module_executions_2026_08 USING btree (gw_session_id, module_name);

