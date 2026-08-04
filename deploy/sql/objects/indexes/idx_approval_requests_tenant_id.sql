--
-- Name: idx_approval_requests_tenant_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_requests_tenant_id ON public.approval_requests USING btree (tenant_id);

