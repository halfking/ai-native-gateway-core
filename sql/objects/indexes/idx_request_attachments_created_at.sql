--
-- Name: idx_request_attachments_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_attachments_created_at ON public.request_attachments USING btree (created_at DESC);

