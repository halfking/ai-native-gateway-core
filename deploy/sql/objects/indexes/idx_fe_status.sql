--
-- Name: idx_fe_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_fe_status ON public.fault_events USING btree (status);

