--
-- Name: idx_task_default_routing_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_task_default_routing_lookup ON public.task_default_routing USING btree (task_type, profile, tenant_id);

