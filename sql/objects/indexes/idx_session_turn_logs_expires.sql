--
-- Name: idx_session_turn_logs_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_turn_logs_expires ON public.session_turn_logs USING btree (expires_at);

