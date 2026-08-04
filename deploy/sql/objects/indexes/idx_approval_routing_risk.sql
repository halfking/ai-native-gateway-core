--
-- Name: idx_approval_routing_risk; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_routing_risk ON public.approval_routing_rules USING btree (tenant_id, risk_level) WHERE (enabled = true);

