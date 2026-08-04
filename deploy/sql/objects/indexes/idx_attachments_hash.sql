--
-- Name: idx_attachments_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_attachments_hash ON public.attachments USING btree (content_hash, tenant_id);

