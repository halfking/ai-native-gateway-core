--
-- Name: idx_fe_detected; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_fe_detected ON public.fault_events USING btree (detected_at DESC);

