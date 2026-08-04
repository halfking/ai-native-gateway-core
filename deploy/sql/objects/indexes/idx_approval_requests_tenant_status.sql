--
-- Name: idx_approval_requests_tenant_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_requests_tenant_status ON public.approval_requests USING btree (tenant_id, status);

