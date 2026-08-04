--
-- Name: idx_session_last_requests_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_last_requests_expires ON public.session_last_requests USING btree (expires_at);

