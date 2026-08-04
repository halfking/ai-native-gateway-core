--
-- Name: idx_system_settings_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_system_settings_updated ON public.system_settings USING btree (updated_at DESC);

