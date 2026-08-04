--
-- Name: idx_feedback_annotated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_feedback_annotated ON public.intent_classification_feedback USING btree (annotated_at DESC) WHERE (annotated_at IS NOT NULL);

