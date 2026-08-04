--
-- Name: idx_approval_approvers_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_approvers_enabled ON public.approval_approvers USING btree (tenant_id, enabled) WHERE (enabled = true);

