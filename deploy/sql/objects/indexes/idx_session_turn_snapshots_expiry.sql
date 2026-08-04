--
-- Name: idx_session_turn_snapshots_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_turn_snapshots_expiry ON public.session_turn_snapshots USING btree (expires_at);

