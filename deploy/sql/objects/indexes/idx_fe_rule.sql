--
-- Name: idx_fe_rule; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_fe_rule ON public.fault_events USING btree (rule_id);

