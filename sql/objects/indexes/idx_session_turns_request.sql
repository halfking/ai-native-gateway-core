--
-- Name: idx_session_turns_request; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_turns_request ON ONLY public.session_turns USING btree (request_id);

