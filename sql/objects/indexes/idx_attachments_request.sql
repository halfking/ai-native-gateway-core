--
-- Name: idx_attachments_request; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_attachments_request ON public.attachments USING btree (request_id);

