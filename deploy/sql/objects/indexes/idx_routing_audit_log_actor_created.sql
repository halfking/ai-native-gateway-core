--
-- Name: idx_routing_audit_log_actor_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_audit_log_actor_created ON public.routing_audit_log USING btree (actor, created_at DESC);

