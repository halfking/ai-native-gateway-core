--
-- Name: idx_routing_overrides_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_routing_overrides_unique ON public.routing_overrides USING btree (task_type, profile, COALESCE(model_chosen, ''::text), mode);

