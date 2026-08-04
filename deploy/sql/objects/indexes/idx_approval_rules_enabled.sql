--
-- Name: idx_approval_rules_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_rules_enabled ON public.approval_rules USING btree (tenant_id, enabled) WHERE (enabled = true);

