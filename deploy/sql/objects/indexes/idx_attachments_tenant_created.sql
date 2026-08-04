--
-- Name: idx_attachments_tenant_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_attachments_tenant_created ON public.attachments USING btree (tenant_id, created_at DESC);

