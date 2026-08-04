--
-- Name: idx_sme_hot_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sme_hot_lookup ON public.session_module_executions_hot USING btree (gw_session_id, module_name, cache_key, status, expires_at) WHERE ((status)::text = 'completed'::text);

