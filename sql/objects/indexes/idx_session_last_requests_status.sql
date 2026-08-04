--
-- Name: idx_session_last_requests_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_last_requests_status ON public.session_last_requests USING btree (last_request_status, updated_at DESC);

