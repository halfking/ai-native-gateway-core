--
-- Name: idx_sticky_sessions_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sticky_sessions_expires ON public.sticky_sessions USING btree (expires_at);

