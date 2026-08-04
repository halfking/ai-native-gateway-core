--
-- Name: idx_approval_requests_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_requests_expires_at ON public.approval_requests USING btree (expires_at) WHERE ((status)::text = 'pending'::text);

