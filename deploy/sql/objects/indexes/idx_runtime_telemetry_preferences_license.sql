--
-- Name: idx_runtime_telemetry_preferences_license; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_runtime_telemetry_preferences_license ON public.runtime_telemetry_preferences USING btree (license_id);

