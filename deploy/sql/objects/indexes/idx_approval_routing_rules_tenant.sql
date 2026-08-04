--
-- Name: idx_approval_routing_rules_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_routing_rules_tenant ON public.approval_routing_rules USING btree (tenant_id, enabled);

