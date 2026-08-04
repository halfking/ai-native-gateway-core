--
-- Name: idx_sessions_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sessions_status ON ONLY public.sessions USING btree (status, updated_at DESC);

