--
-- Name: uq_task_default_routing; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_task_default_routing ON public.task_default_routing USING btree (task_type, profile, tier, COALESCE(tenant_id, ''::character varying));

