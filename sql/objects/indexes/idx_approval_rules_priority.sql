--
-- Name: idx_approval_rules_priority; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_rules_priority ON public.approval_rules USING btree (tenant_id, priority DESC);

