--
-- Name: idx_fal_triggered; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_fal_triggered ON public.fault_action_logs USING btree (triggered_at DESC);

