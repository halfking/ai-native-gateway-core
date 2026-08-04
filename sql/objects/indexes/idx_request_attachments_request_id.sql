--
-- Name: idx_request_attachments_request_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_attachments_request_id ON public.request_attachments USING btree (request_id);

