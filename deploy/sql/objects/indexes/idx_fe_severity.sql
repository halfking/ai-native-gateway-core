--
-- Name: idx_fe_severity; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_fe_severity ON public.fault_events USING btree (severity);

