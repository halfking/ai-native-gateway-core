--
-- Name: idx_fal_event; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_fal_event ON public.fault_action_logs USING btree (event_id);

