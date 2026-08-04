--
-- Name: idx_approval_approvers_priority; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_approvers_priority ON public.approval_approvers USING btree (tenant_id, priority DESC);

