--
-- Name: idx_rae_instance_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rae_instance_status ON public.runtime_alert_events USING btree (instance_id, status, detected_at DESC);

