--
-- Name: idx_approval_requests_request_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_approval_requests_request_id ON public.approval_requests USING btree (request_id);

