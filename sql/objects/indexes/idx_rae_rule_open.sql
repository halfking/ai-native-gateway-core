--
-- Name: idx_rae_rule_open; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rae_rule_open ON public.runtime_alert_events USING btree (rule_key, instance_id) WHERE (status = ANY (ARRAY['triggered'::text, 'acknowledged'::text, 'suppressed'::text]));

