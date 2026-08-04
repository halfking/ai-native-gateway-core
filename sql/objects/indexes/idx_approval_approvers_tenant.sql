--
-- Name: idx_approval_approvers_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_approvers_tenant ON public.approval_approvers USING btree (tenant_id);

