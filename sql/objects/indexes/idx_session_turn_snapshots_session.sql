--
-- Name: idx_session_turn_snapshots_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_turn_snapshots_session ON public.session_turn_snapshots USING btree (tenant_id, gw_session_id, turn_no DESC);

