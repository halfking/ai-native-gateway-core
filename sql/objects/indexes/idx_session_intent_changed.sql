--
-- Name: idx_session_intent_changed; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_intent_changed ON public.session_intent_evolution USING btree (is_intent_changed, tenant_id) WHERE (is_intent_changed = true);

