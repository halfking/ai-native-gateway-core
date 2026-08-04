--
-- Name: idx_session_turns_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_turns_tenant ON ONLY public.session_turns USING btree (tenant_id, ts DESC);

