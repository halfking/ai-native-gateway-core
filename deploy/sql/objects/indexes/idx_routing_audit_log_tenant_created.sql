--
-- Name: idx_routing_audit_log_tenant_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_audit_log_tenant_created ON public.routing_audit_log USING btree (tenant_id, created_at DESC);

