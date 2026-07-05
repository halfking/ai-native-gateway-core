--
-- Name: idx_settings_kv_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_kv_updated ON public.settings_kv USING btree (updated_at DESC);

