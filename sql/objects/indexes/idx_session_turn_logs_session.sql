--
-- Name: idx_session_turn_logs_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_turn_logs_session ON public.session_turn_logs USING btree (session_id, turn_no, started_at DESC);

