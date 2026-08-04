--
-- Name: idx_intent_config_strategy; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_intent_config_strategy ON public.intent_classifier_config USING btree (strategy, updated_at DESC);

