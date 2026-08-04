--
-- Name: idx_feedback_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_feedback_session ON public.intent_classification_feedback USING btree (session_id, created_at DESC);

