--
-- Name: idx_request_attachments_status_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_attachments_status_time ON public.request_attachments USING btree (status, created_at DESC);

