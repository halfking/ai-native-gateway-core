--
-- Name: idx_approval_requests_session_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_requests_session_id ON public.approval_requests USING btree (session_id);

