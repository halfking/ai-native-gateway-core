--
-- Name: idx_approval_requests_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_requests_created_at ON public.approval_requests USING btree (created_at DESC);

