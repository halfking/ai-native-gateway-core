--
-- Name: idx_runtime_telemetry_consent_events_hardware_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_runtime_telemetry_consent_events_hardware_time ON public.runtime_telemetry_consent_events USING btree (hardware_hash, occurred_at DESC);

