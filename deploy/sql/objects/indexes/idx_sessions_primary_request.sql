--
-- Name: idx_sessions_primary_request; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sessions_primary_request ON ONLY public.sessions USING btree (primary_request_id) WHERE (primary_request_id IS NOT NULL);

