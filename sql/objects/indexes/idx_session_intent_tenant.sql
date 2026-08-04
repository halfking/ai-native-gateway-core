--
-- Name: idx_session_intent_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_intent_tenant ON public.session_intent_evolution USING btree (tenant_id, classified_at DESC);

