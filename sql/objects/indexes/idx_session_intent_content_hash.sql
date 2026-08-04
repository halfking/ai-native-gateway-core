--
-- Name: idx_session_intent_content_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_intent_content_hash ON public.session_intent_evolution USING btree (user_content_hash) WHERE (user_content_hash IS NOT NULL);

