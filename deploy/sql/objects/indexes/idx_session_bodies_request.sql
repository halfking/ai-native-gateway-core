--
-- Name: idx_session_bodies_request; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_bodies_request ON ONLY public.session_bodies USING btree (request_id);

