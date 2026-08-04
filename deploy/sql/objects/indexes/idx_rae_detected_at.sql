--
-- Name: idx_rae_detected_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rae_detected_at ON public.runtime_alert_events USING btree (detected_at DESC);

