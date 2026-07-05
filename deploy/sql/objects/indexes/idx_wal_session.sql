--
-- Name: idx_wal_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_wal_session ON ONLY public.request_wal USING btree (gw_session_id, created_at);

