--
-- Name: idx_session_turn_logs_request; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_turn_logs_request ON public.session_turn_logs USING btree (request_id);

