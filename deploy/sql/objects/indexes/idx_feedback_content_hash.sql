--
-- Name: idx_feedback_content_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_feedback_content_hash ON public.intent_classification_feedback USING btree (user_content_hash, predicted_intent) WHERE (user_content_hash IS NOT NULL);

