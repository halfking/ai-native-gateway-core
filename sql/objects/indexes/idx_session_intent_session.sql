--
-- Name: idx_session_intent_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_intent_session ON public.session_intent_evolution USING btree (session_id, turn_number DESC);

