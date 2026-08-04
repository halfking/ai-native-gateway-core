--
-- Name: idx_sessions_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sessions_tenant ON ONLY public.sessions USING btree (tenant_id, created_at DESC);

