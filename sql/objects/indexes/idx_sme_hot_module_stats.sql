--
-- Name: idx_sme_hot_module_stats; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sme_hot_module_stats ON public.session_module_executions_hot USING btree (module_name, status, completed_at DESC);

