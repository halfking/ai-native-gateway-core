--
-- Name: idx_session_turns_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_turns_session ON ONLY public.session_turns USING btree (session_id, turn_no DESC);

