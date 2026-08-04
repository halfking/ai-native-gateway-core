--
-- Name: idx_approval_queue_tenant_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_queue_tenant_pending ON public.approval_queue USING btree (tenant_id, created_at DESC) WHERE (status = 'pending'::text);

