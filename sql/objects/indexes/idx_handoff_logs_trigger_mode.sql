--
-- Name: idx_handoff_logs_trigger_mode; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_handoff_logs_trigger_mode ON public.handoff_logs USING btree (trigger_reason, created_at DESC);

