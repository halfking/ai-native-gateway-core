--
-- Name: idx_sessions_session_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sessions_session_id ON ONLY public.sessions USING btree (session_id);

