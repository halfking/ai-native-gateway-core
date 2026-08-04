--
-- Name: idx_sme_hot_cleanup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sme_hot_cleanup ON public.session_module_executions_hot USING btree (created_at);

