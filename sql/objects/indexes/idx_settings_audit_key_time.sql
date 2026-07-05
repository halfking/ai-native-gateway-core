--
-- Name: idx_settings_audit_key_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_audit_key_time ON public.settings_audit USING btree (setting_key, created_at DESC);

