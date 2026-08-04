--
-- Name: idx_session_bodies_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_bodies_session ON ONLY public.session_bodies USING btree (session_id, turn_no DESC);

