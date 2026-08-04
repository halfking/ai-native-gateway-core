--
-- Name: idx_feedback_correct; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_feedback_correct ON public.intent_classification_feedback USING btree (is_correct, predicted_intent, tenant_id) WHERE (is_correct IS NOT NULL);

