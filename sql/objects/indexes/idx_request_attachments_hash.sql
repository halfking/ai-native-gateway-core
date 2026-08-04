--
-- Name: idx_request_attachments_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_attachments_hash ON public.request_attachments USING btree (hash) WHERE (hash IS NOT NULL);

