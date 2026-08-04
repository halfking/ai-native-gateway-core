--
-- Name: idx_session_intent_primary; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_intent_primary ON public.session_intent_evolution USING btree (primary_intent, tenant_id, classified_at DESC);

