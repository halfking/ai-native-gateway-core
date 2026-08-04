--
-- Name: idx_feedback_unannotated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_feedback_unannotated ON public.intent_classification_feedback USING btree (predicted_confidence, created_at DESC) WHERE (actual_intent IS NULL);

